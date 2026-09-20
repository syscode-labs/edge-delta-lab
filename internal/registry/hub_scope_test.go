package registry

import (
	"context"
	"testing"

	"example.com/edge-delta-lab/internal/testtls"
)

// A local-root publisher must not apply hub credentials to discovery, token
// exchange, or image export. The registry has its own transport.
func TestHubCredentialsDoNotAffectRegistryPublication(t *testing.T) {
	f, _, digest := startServingRegistry(t, "proj/app", "v1")
	cfg, _ := testCfg(t, f.srv.URL, []RepoConfig{{Name: "proj/app"}})
	cfg.HubTLS = testtls.New(t).Client
	client, err := NewClient(cfg.RegistryURL, AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.HTTP.CloseIdleConnections()
	watcher, err := NewWatcher(client, cfg)
	if err != nil {
		t.Fatal(err)
	}
	requests, err := watcher.PollOnce(context.Background())
	if err != nil || len(requests) != 1 {
		t.Fatalf("discovery: %v, requests=%v", err, requests)
	}
	if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, PublishRequest{Repo: "proj/app", Tag: "v1", Digest: digest}); err != nil {
		t.Fatalf("export with unrelated hub identity: %v", err)
	}
}
