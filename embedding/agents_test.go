package embedding_test

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/edge-delta-lab/hubclient"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/push"
	"example.com/edge-delta-lab/internal/testtls"
	"example.com/edge-delta-lab/publisher"
	"example.com/edge-delta-lab/receiver"
	"github.com/google/go-containerregistry/pkg/name"
	registryserver "github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

func pubConfig(t *testing.T, url string) publisher.Config {
	t.Helper()
	dir := t.TempDir()
	keys := filepath.Join(dir, "keys")
	if err := lab.Keygen(keys); err != nil {
		t.Fatal(err)
	}
	return publisher.Config{RegistryURL: url, Repositories: []publisher.Repository{{Name: "app"}}, Poll: time.Hour, StateFile: filepath.Join(dir, "watch.json"), Root: filepath.Join(dir, "origin"), SigningKey: filepath.Join(keys, "publisher.key"), Channel: "desired", SequenceFile: filepath.Join(dir, "sequence")}
}
func runUntilCanceled(t *testing.T, run func(context.Context) error, ctx context.Context) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- run(ctx) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("run: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent did not stop")
	}
}

func TestPublisherReceiverMutualTLS(t *testing.T) {
	m := testtls.New(t)
	reg := registryserver.New(registryserver.Logger(log.New(io.Discard, "", 0)))
	seed := httptest.NewServer(reg)
	img, err := random.Image(1024, 1)
	if err != nil {
		t.Fatal(err)
	}
	ref, err := name.NewTag(strings.TrimPrefix(seed.URL, "http://")+"/app:v1", name.Insecure)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.Write(ref, img); err != nil {
		t.Fatal(err)
	}
	defer seed.Close()
	pc := pubConfig(t, seed.URL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pc.Event = func(kind, detail string) {
		if kind == "release-published" {
			cancel()
		} else if kind == "watcher-error" {
			t.Errorf("publish: %s", detail)
			cancel()
		}
	}
	p, err := publisher.New(pc)
	if err != nil {
		t.Fatal(err)
	}
	runUntilCanceled(t, p.Run, ctx)
	if _, err := os.Stat(pc.StateFile); err != nil {
		t.Fatalf("acknowledged state missing: %v", err)
	}

	origin := lab.NewFaultServer(pc.Root, lab.FaultPlan{})
	origin.ReceiptsDir = filepath.Join(t.TempDir(), "receipts")
	broker := push.NewBroker()
	origin.Events = broker.Handler()
	hub := httptest.NewUnstartedServer(origin)
	hub.TLS = m.Server.Clone()
	hub.StartTLS()
	defer hub.Close()
	rc := receiver.DefaultConfig()
	rc.ManifestURL = hub.URL + "/releases/desired.json"
	rc.BaseURL = hub.URL
	rc.HubTLS = m.Client
	rc.StateDir = t.TempDir()
	rc.PublicKey = filepath.Join(filepath.Dir(pc.SigningKey), "publisher.pub")
	rc.ReserveBytes = 0
	rc.ReceiptURL = hub.URL + "/receipts"
	rc.DeviceID = "test-device"
	rc.EventsURL = strings.Replace(hub.URL, "https:", "wss:", 1) + "/events"
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	var result receiver.Result
	rc.OnSync = func(s receiver.Result) { result = s; cancel2() }
	rc.OnError = func(err error) { t.Errorf("receiver: %v", err); cancel2() }
	r, err := receiver.New(rc)
	if err != nil {
		t.Fatal(err)
	}
	runUntilCanceled(t, r.Run, ctx)
	if result.Phase != "staged" {
		t.Fatalf("phase=%q", result.Phase)
	}
	if _, err := os.Stat(result.Artifact); err != nil {
		t.Fatal(err)
	}
	receipts, err := os.ReadDir(origin.ReceiptsDir)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt not delivered over mTLS: %v (%d)", err, len(receipts))
	}
}

func TestAgentsDeniedTLSAndFailureCancellation(t *testing.T) {
	m := testtls.New(t)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unauthenticated agent reached handler") }))
	srv.TLS = m.Server
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	pc := pubConfig(t, srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	failed := false
	pc.Event = func(kind, detail string) {
		if kind == "watcher-error" {
			failed = true
			cancel()
		}
	}
	p, err := publisher.New(pc)
	if err != nil {
		t.Fatal(err)
	}
	runUntilCanceled(t, p.Run, ctx)
	if !failed {
		t.Fatal("publisher did not report denial")
	}
	if _, err := os.Stat(pc.StateFile); !os.IsNotExist(err) {
		t.Fatal("denied publication acknowledged")
	}
	rc := receiver.DefaultConfig()
	rc.ManifestURL = srv.URL + "/releases/desired.json"
	rc.BaseURL = srv.URL
	rc.StateDir = t.TempDir()
	rc.PublicKey = filepath.Join(filepath.Dir(pc.SigningKey), "publisher.pub")
	rc.HubTLS = hubclient.Config{CA: m.Client.CA}
	rc.MaxAttempts = 1
	ctx, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	failed = false
	rc.OnError = func(err error) { failed = true; cancel2() }
	r, err := receiver.New(rc)
	if err != nil {
		t.Fatal(err)
	}
	runUntilCanceled(t, r.Run, ctx)
	if !failed {
		t.Fatal("receiver did not report denial")
	}
	if _, err := os.Stat(filepath.Join(rc.StateDir, "state.json")); !os.IsNotExist(err) {
		t.Fatal("denied receiver changed accepted state")
	}
}
