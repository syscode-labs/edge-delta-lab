package hubclient_test

import (
	"context"
	"errors"
	"example.com/edge-delta-lab/hubclient"
	"example.com/edge-delta-lab/internal/testtls"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMutualTLSAllowedAndDenied(t *testing.T) {
	m := testtls.New(t)
	other := testtls.New(t)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	s.TLS = m.Server
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	s.StartTLS()
	defer s.Close()
	for _, tc := range []struct {
		name   string
		config hubclient.Config
		ok     bool
	}{
		{"allowed", m.Client, true},
		{"no-client", hubclient.Config{CA: m.Client.CA}, false},
		{"wrong-server-ca", other.Client, false},
		{"untrusted-client", hubclient.Config{CA: m.Client.CA, ClientCert: other.Client.ClientCert, ClientKey: other.Client.ClientKey}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, err := hubclient.New(tc.config, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.CloseIdleConnections()
			resp, err := c.Get(s.URL)
			if resp != nil {
				resp.Body.Close()
			}
			if (err == nil) != tc.ok {
				t.Fatalf("success=%v want %v: %v", err == nil, tc.ok, err)
			}
		})
	}
	c, err := hubclient.New(m.Client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	if resp, err := c.Get(strings.Replace(s.URL, "127.0.0.1", "localhost", 1)); err == nil {
		resp.Body.Close()
		t.Fatal("hostname mismatch accepted")
	}
}

func TestConfigurationAndDowngradeFailClosed(t *testing.T) {
	m := testtls.New(t)
	if err := os.Chmod(m.Client.ClientKey, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Client.TLSConfig(); err == nil {
		t.Fatal("world readable key accepted")
	}
	if _, err := (hubclient.Config{ClientCert: m.Client.ClientCert}).TLSConfig(); err == nil {
		t.Fatal("partial pair accepted")
	}
	if _, err := (hubclient.Config{CA: m.Client.ClientKey}).TLSConfig(); err == nil {
		t.Fatal("non CA accepted")
	}
	if err := os.Chmod(m.Client.ClientKey, 0600); err != nil {
		t.Fatal(err)
	}
	var reached atomic.Int32
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1) }))
	defer plain.Close()
	tlsServer := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, plain.URL, 302) }))
	tlsServer.TLS = m.Server
	tlsServer.StartTLS()
	defer tlsServer.Close()
	c, err := hubclient.New(m.Client, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	for _, u := range []string{plain.URL, tlsServer.URL} {
		if resp, err := c.Get(u); err == nil {
			resp.Body.Close()
			t.Fatal("plaintext or redirect accepted")
		}
	}
	if reached.Load() != 0 {
		t.Fatal("plaintext endpoint reached")
	}
}

func TestRequestCancellation(t *testing.T) {
	started := make(chan struct{})
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
	defer s.Close()
	c, err := hubclient.New(hubclient.Config{}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseIdleConnections()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { r, _ := http.NewRequestWithContext(ctx, "GET", s.URL, nil); _, err := c.Do(r); done <- err }()
	<-started
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation stuck")
	}
}
