package registry

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-containerregistry/pkg/v1/random"
)

func TestWatcherRunRetriesFailedPublication(t *testing.T) {
	f, _, _ := startServingRegistry(t, "proj/app", "v1")
	cfg, _ := testCfg(t, f.srv.URL, []RepoConfig{{Name: "proj/app"}})
	w, err := NewWatcher(mustClient(t, f.srv.URL), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	attempts := 0
	err = w.Run(ctx, func(ctx context.Context, req PublishRequest) error {
		attempts++
		if len(w.Status()) != 0 {
			t.Error("digest consumed before successful publication")
		}
		if attempts == 1 {
			return errors.New("transient export failure")
		}
		_, err := Trigger(ctx, TriggerOptions{Config: cfg}, req)
		cancel()
		return err
	})
	if !errors.Is(err, context.Canceled) || attempts != 2 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	if len(w.Status()) != 1 {
		t.Fatal("successful publication was not acknowledged")
	}
	if reqs, err := w.PollOnce(context.Background()); err != nil || len(reqs) != 0 {
		t.Fatalf("successful release re-emitted: %v %v", reqs, err)
	}
}

func TestWatcherPublicationFailureRestart(t *testing.T) {
	for _, stage := range []string{"export", "sign", "promote", "state"} {
		t.Run(stage, func(t *testing.T) {
			const repo, tag = "proj/app", "v1"
			f, img, digest := startServingRegistry(t, repo, tag)
			cfg, _ := testCfg(t, f.srv.URL, []RepoConfig{{Name: repo}})
			req := PublishRequest{Repo: repo, Tag: tag, Digest: digest}
			release, err := releaseName(cfg.Publish.ReleasePrefix, req)
			if err != nil {
				t.Fatal(err)
			}
			releasePath := filepath.Join(cfg.Publish.Root, "releases", release+".json")
			channel := filepath.Join(cfg.Publish.Root, "releases", cfg.Publish.Channel+".json")
			var restore func()
			switch stage {
			case "export":
				f.serveImage(repo, nil)
				restore = func() { f.serveImage(repo, img) }
			case "sign":
				key := mustRead(t, cfg.Publish.Key)
				if err := os.Remove(cfg.Publish.Key); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.WriteFile(cfg.Publish.Key, key, 0600); err != nil {
						t.Fatal(err)
					}
				}
			case "promote":
				if err := os.MkdirAll(channel, 0755); err != nil {
					t.Fatal(err)
				}
				restore = func() {
					if err := os.Remove(channel); err != nil {
						t.Fatal(err)
					}
				}
			case "state":
				// Created after NewWatcher below, so loading state itself remains valid.
				restore = func() {
					if err := os.Remove(cfg.StateFile); err != nil {
						t.Fatal(err)
					}
				}
			}
			w, err := NewWatcher(mustClient(t, f.srv.URL), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if stage == "state" {
				if err := os.Mkdir(cfg.StateFile, 0755); err != nil {
					t.Fatal(err)
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			// Run exactly one attempt, including its acknowledge step.
			_ = w.Run(ctx, func(ctx context.Context, r PublishRequest) error {
				_, err := Trigger(ctx, TriggerOptions{Config: cfg}, r)
				cancel()
				if stage != "state" && err == nil {
					t.Error("injected publication failure did not fail")
				}
				if stage == "state" && err != nil {
					t.Errorf("publish before state failure: %v", err)
				}
				return err
			})
			if len(w.Status()) != 0 {
				t.Fatal("failed publication/state write consumed digest")
			}
			var checkpoint, checkpointSequence []byte
			if stage == "promote" || stage == "state" {
				checkpoint = mustRead(t, releasePath)
				checkpointSequence = mustRead(t, cfg.Publish.SequenceFile)
			}
			restore()
			// Failed work must survive a new process loading only the persisted state.
			w, err = NewWatcher(mustClient(t, f.srv.URL), cfg)
			if err != nil {
				t.Fatal(err)
			}
			pending, err := w.PollOnce(context.Background())
			if err != nil || len(pending) != 1 || pending[0] != req {
				t.Fatalf("restart pending=%v err=%v", pending, err)
			}
			if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, req); err != nil {
				t.Fatalf("retry: %v", err)
			}
			if err := w.Acknowledge(req); err != nil {
				t.Fatal(err)
			}
			envelope := mustRead(t, releasePath)
			if checkpoint != nil && !bytes.Equal(checkpoint, envelope) {
				t.Fatal("retry re-signed the checkpoint")
			}
			if !bytes.Equal(mustRead(t, channel), envelope) {
				t.Fatal("channel not promoted")
			}
			sequence := mustRead(t, cfg.Publish.SequenceFile)
			if checkpointSequence != nil && !bytes.Equal(checkpointSequence, sequence) {
				t.Fatal("checkpoint retry allocated another sequence")
			}
			// Trigger is independently idempotent across a lost acknowledgment/restart.
			if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, req); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(sequence, mustRead(t, cfg.Publish.SequenceFile)) || !bytes.Equal(envelope, mustRead(t, releasePath)) {
				t.Fatal("duplicate publication changed signed release or sequence")
			}
			w, err = NewWatcher(mustClient(t, f.srv.URL), cfg)
			if err != nil {
				t.Fatal(err)
			}
			if pending, err := w.PollOnce(context.Background()); err != nil || len(pending) != 0 {
				t.Fatalf("published digest re-emitted on restart: %v %v", pending, err)
			}
		})
	}
}

func TestTriggerChangedTagPreservesReleaseHistory(t *testing.T) {
	const repo, tag = "proj/app", "v1"
	f, _, digest := startServingRegistry(t, repo, tag)
	cfg, _ := testCfg(t, f.srv.URL, []RepoConfig{{Name: repo}})
	req := PublishRequest{Repo: repo, Tag: tag, Digest: digest}
	first, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, req)
	if err != nil {
		t.Fatal(err)
	}
	firstPath := filepath.Join(cfg.Publish.Root, "releases", first+".json")
	envelope := mustRead(t, firstPath)
	img, err := random.Image(256, 2)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	f.setManifest(repo, tag, string(manifest))
	f.serveImage(repo, img)
	hash, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	req.Digest = hash.String()
	second, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, req)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("tag movement reused immutable release name")
	}
	if !bytes.Equal(envelope, mustRead(t, firstPath)) {
		t.Fatal("first release changed")
	}
	if string(mustRead(t, cfg.Publish.SequenceFile)) != "2\n" {
		t.Fatal("sequence history is not monotonic")
	}
	channel := filepath.Join(cfg.Publish.Root, "releases", cfg.Publish.Channel+".json")
	latest := mustRead(t, channel)
	req.Digest = digest
	if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, req); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(latest, mustRead(t, channel)) {
		t.Fatal("retry rolled back the channel")
	}
	if string(mustRead(t, cfg.Publish.SequenceFile)) != "2\n" {
		t.Fatal("retry consumed another sequence")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
