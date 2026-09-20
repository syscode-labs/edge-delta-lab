package embedding_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/edge-delta-lab/hubclient"
	"example.com/edge-delta-lab/publisher"
	"example.com/edge-delta-lab/receiver"
)

// hostTransport models instrumentation that also attaches a host identity.
// These tests must not run in parallel: they temporarily replace net/http globals.
type hostTransport struct {
	base          http.RoundTripper
	calls, closes atomic.Int32
}

func (h *hostTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h.calls.Add(1)
	r = r.Clone(r.Context())
	r.Header.Set("X-Host-Identity", "host-only")
	return h.base.RoundTrip(r)
}
func (h *hostTransport) CloseIdleConnections() { h.closes.Add(1) }

// This is deliberately a separate Go module: no internal imports or CLI calls.
func TestExternalModuleStartsAndCancelsBothAgents(t *testing.T) {
	original := http.DefaultTransport
	host := &hostTransport{base: original}
	http.DefaultTransport = host
	defer func() { http.DefaultTransport = original }()
	// Construction and shutdown must also work independently of either agent.
	client, err := hubclient.New(hubclient.Config{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	client.CloseIdleConnections()
	dir := t.TempDir()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath, pubPath := filepath.Join(dir, "key"), filepath.Join(dir, "pub")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pubPath, []byte(hex.EncodeToString(pub)), 0600); err != nil {
		t.Fatal(err)
	}
	reached := make(chan string, 2)
	var pr, rr sync.Once
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Host-Identity") != "" {
			t.Error("host identity leaked to agent request")
		}
		if r.URL.Path == "/v2/app/tags/list" {
			pr.Do(func() { reached <- "publisher" })
		} else {
			rr.Do(func() { reached <- "receiver" })
		}
		<-r.Context().Done()
	}))
	defer s.Close()
	// Assert before httptest.Server.Close, which itself closes DefaultTransport.
	defer func() {
		if host.calls.Load() != 0 || host.closes.Load() != 0 {
			t.Errorf("agents used host transport: requests=%d closes=%d", host.calls.Load(), host.closes.Load())
		}
	}()
	p, err := publisher.New(publisher.Config{RegistryURL: s.URL, Repositories: []publisher.Repository{{Name: "app"}}, StateFile: filepath.Join(dir, "watch.json"), Root: filepath.Join(dir, "root"), SigningKey: keyPath, Channel: "desired", SequenceFile: filepath.Join(dir, "seq")})
	if err != nil {
		t.Fatal(err)
	}
	c := receiver.DefaultConfig()
	c.BaseURL = s.URL
	c.ManifestURL = s.URL + "/releases/desired.json"
	c.StateDir = filepath.Join(dir, "receiver")
	c.PublicKey = pubPath
	c.AllowHTTP = true
	r, err := receiver.New(c)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 2)
	go func() { done <- p.Run(ctx) }()
	go func() { done <- r.Run(ctx) }()
	for i := 0; i < 2; i++ {
		select {
		case <-reached:
		case <-time.After(5 * time.Second):
			cancel()
			t.Fatal("agent never reached transport")
		}
	}
	if err := p.Run(ctx); err == nil {
		t.Error("concurrent publisher Run allowed")
	}
	if err := r.Run(ctx); err == nil {
		t.Error("concurrent receiver Run allowed")
	}
	cancel()
	for i := 0; i < 2; i++ {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("shutdown=%v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("shutdown blocked")
		}
	}
	// Objects can be reused after Run returns, including pre-canceled contexts.
	if err := p.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := r.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestExternalModuleDoesNotInheritHostTLS(t *testing.T) {
	var requests, identities atomic.Int32
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if len(r.TLS.PeerCertificates) > 0 {
			identities.Add(1)
		}
		w.Write([]byte(`{"name":"app","tags":[]}`))
	}))
	s.TLS = &tls.Config{ClientAuth: tls.RequestClientCert}
	s.StartTLS()
	defer s.Close()
	roots := x509.NewCertPool()
	roots.AddCert(s.Certificate())
	host := &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: roots, Certificates: s.TLS.Certificates,
	}}
	defer host.CloseIdleConnections()
	original, originalClient := http.DefaultTransport, http.DefaultClient
	http.DefaultTransport = host
	http.DefaultClient = &http.Client{Transport: host}
	defer func() { http.DefaultTransport, http.DefaultClient = original, originalClient }()
	// Prove the host's private trust and identity work before testing isolation.
	resp, err := http.DefaultClient.Get(s.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if requests.Load() != 1 || identities.Load() != 1 {
		t.Fatal("host TLS fixture did not send identity")
	}

	client, err := hubclient.New(hubclient.Config{}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	resp, err = client.Get(s.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("hub inherited private host trust")
	}
	var unknown x509.UnknownAuthorityError
	if !errors.As(err, &unknown) {
		t.Fatalf("hub rejection was not system trust: %v", err)
	}

	dir := t.TempDir()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(key)), 0600); err != nil {
		t.Fatal(err)
	}
	events := make(chan string, 8)
	p, err := publisher.New(publisher.Config{
		RegistryURL: s.URL, Repositories: []publisher.Repository{{Name: "app"}},
		StateFile: filepath.Join(dir, "watch.json"), Root: filepath.Join(dir, "root"),
		SigningKey: keyPath, Channel: "desired", SequenceFile: filepath.Join(dir, "seq"),
		Event: func(kind, detail string) {
			select {
			case events <- detail:
			default:
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("publisher shutdown blocked")
		}
	}()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case detail := <-events:
			if strings.Contains(detail, "certificate signed by unknown authority") {
				if requests.Load() != 1 || identities.Load() != 1 {
					t.Fatal("registry inherited host TLS trust or identity")
				}
				return
			}
		case <-timer.C:
			t.Fatal("registry did not reject private host trust")
		}
	}
}
