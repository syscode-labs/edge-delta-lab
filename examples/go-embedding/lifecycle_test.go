package embedding_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"example.com/edge-delta-lab/publisher"
	"example.com/edge-delta-lab/receiver"
)

// This is deliberately a separate Go module: no internal imports or CLI calls.
func TestExternalModuleStartsAndCancelsBothAgents(t *testing.T) {
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
		if r.URL.Path == "/v2/app/tags/list" {
			pr.Do(func() { reached <- "publisher" })
		} else {
			rr.Do(func() { reached <- "receiver" })
		}
		<-r.Context().Done()
	}))
	defer s.Close()
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
