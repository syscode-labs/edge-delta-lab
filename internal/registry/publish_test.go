package registry

import (
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"example.com/edge-delta-lab/internal/lab"
)

// startServingRegistry seeds a real in-memory registry (go-containerregistry
// remote.Registry compatible) with a random image and serves it over the fake
// v2 front so both the watcher client and remote.Image can read it.
func startServingRegistry(t *testing.T, repo, tag string) (*fakeRegistry, v1.Image, string) {
	t.Helper()
	f := &fakeRegistry{manifests: map[string]string{}}
	img, err := random.Image(256, 3)
	if err != nil {
		t.Fatal(err)
	}
	dg, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	// Serve the image through the same fake: manifest + config + layers.
	mb, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	f.setManifest(repo, tag, string(mb))
	srv := f.start(false)
	t.Cleanup(srv.Close)

	// Populate blobs lazily: point the fake's manifest handler for this repo
	// at the raw bytes we already have and add a blob endpoint.
	f.serveImage(repo, img)
	return f, img, dg.String()
}

func TestTriggerPublishesSignedReleaseAndChannel(t *testing.T) {
	const repo, tag = "proj/app", "v1"
	f, _, digest := startServingRegistry(t, repo, tag)

	cfg, dir := testCfg(t, f.srv.URL, []RepoConfig{{Name: repo}})
	_ = dir

	release, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, PublishRequest{Repo: repo, Tag: tag, Digest: digest})
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}

	// The signed release must exist and verify with the publisher key.
	env, err := os.ReadFile(filepath.Join(cfg.Publish.Root, "releases", release+".json"))
	if err != nil {
		t.Fatal(err)
	}
	pubBytes, err := lab.ReadKey(filepath.Join(filepath.Dir(cfg.Publish.Key), "publisher.pub"), ed25519.PublicKeySize)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := lab.Verify(env, ed25519.PublicKey(pubBytes), 1<<50); err != nil {
		t.Fatalf("release envelope does not verify: %v", err)
	}
	// Channel promotion points at the same envelope.
	ch, err := os.ReadFile(filepath.Join(cfg.Publish.Root, "releases", cfg.Publish.Channel+".json"))
	if err != nil {
		t.Fatalf("channel file: %v", err)
	}
	if string(ch) != string(env) {
		t.Fatal("channel envelope differs from release envelope")
	}
	// Sequence file advanced monotonically.
	b, err := os.ReadFile(cfg.Publish.SequenceFile)
	if err != nil || string(b) != "1\n" {
		t.Fatalf("sequence file: %q %v", b, err)
	}
}

func TestTriggerDigestDriftFails(t *testing.T) {
	const repo, tag = "proj/app", "v1"
	f, _, _ := startServingRegistry(t, repo, tag)
	cfg, _ := testCfg(t, f.srv.URL, []RepoConfig{{Name: repo}})
	if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, PublishRequest{Repo: repo, Tag: tag, Digest: "sha256:" + pad64("deadbeef")}); err == nil {
		t.Fatal("expected digest drift to fail")
	}
}

func pad64(s string) string {
	for len(s) < 64 {
		s += "0"
	}
	return s
}
