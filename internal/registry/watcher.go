package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// WatchStatusEntry is one repo:tag with its last-seen digest, reported by
// Watcher.Status for the admin socket's watch-status method.
type WatchStatusEntry struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

// PublishRequest is emitted when an eligible tag's digest is new or changed.
type PublishRequest struct {
	Repo   string `json:"repo"`
	Tag    string `json:"tag"`
	Digest string `json:"digest"`
}

// Watcher polls a registry and emits PublishRequests on digest changes.
type Watcher struct {
	client   *Client
	cfg      Config
	now      func() time.Time // injectable clock
	onEvent  func(kind, detail string) // watcher-error / lifecycle events
	onNotice func(msg string)

	mu    sync.Mutex
	state map[string]string // "repo\x00tag" -> digest
}

// WatcherOption customizes a Watcher for tests.
type WatcherOption func(*Watcher)

// WithClock overrides the time source.
func WithClock(f func() time.Time) WatcherOption { return func(w *Watcher) { w.now = f } }

// WithEventSink receives (kind, detail) events, e.g. ("watcher-error", "...").
func WithEventSink(f func(kind, detail string)) WatcherOption {
	return func(w *Watcher) { w.onEvent = f }
}

// NewWatcher builds a watcher over cfg, loading persisted digest state.
func NewWatcher(c *Client, cfg Config, opts ...WatcherOption) (*Watcher, error) {
	w := &Watcher{
		client:   c,
		cfg:      cfg,
		now:      time.Now,
		onNotice: func(string) {},
		onEvent: func(kind, detail string) {
			fmt.Printf("edgelab watcher event kind=%s detail=%s\n", kind, detail)
		},
		state: map[string]string{},
	}
	for _, o := range opts {
		o(w)
	}
	if err := w.loadState(); err != nil {
		return nil, err
	}
	return w, nil
}

func stateKey(repo, tag string) string { return repo + "\x00" + tag }

// WatchStatus reports the current digest state as one entry per repo:tag.
// Digests the watcher has never seen are absent until their first poll.
func (w *Watcher) Status() []WatchStatusEntry {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([]WatchStatusEntry, 0, len(w.state))
	for k, digest := range w.state {
		repo, tag, _ := strings.Cut(k, "\x00")
		out = append(out, WatchStatusEntry{Repo: repo, Tag: tag, Digest: digest})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Repo != out[j].Repo {
			return out[i].Repo < out[j].Repo
		}
		return out[i].Tag < out[j].Tag
	})
	return out
}

func (w *Watcher) loadState() error {
	b, err := readFile(w.cfg.StateFile)
	if err != nil {
		return nil // first run: no state yet
	}
	var st struct {
		Digests map[string]string `json:"digests"`
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return fmt.Errorf("watcher state %s: %w", w.cfg.StateFile, err)
	}
	w.state = st.Digests
	if w.state == nil {
		w.state = map[string]string{}
	}
	return nil
}

// persist writes digest state atomically via lab.AtomicWrite semantics.
func (w *Watcher) persist() error {
	w.mu.Lock()
	digests := make(map[string]string, len(w.state))
	for k, v := range w.state {
		digests[k] = v
	}
	w.mu.Unlock()
	b, err := json.MarshalIndent(struct {
		Digests map[string]string `json:"digests"`
	}{Digests: digests}, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(w.cfg.StateFile, b, 0o600)
}

// PollOnce scans all repos once, emitting publish requests for new digests.
// Returns the emitted requests.
func (w *Watcher) PollOnce(ctx context.Context) ([]PublishRequest, error) {
	var emitted []PublishRequest
	var firstErr error
	for _, repo := range w.cfg.Repos {
		tags, err := w.client.Tags(ctx, repo.Name)
		if err != nil {
			w.emitError(fmt.Sprintf("tags/list %s: %v", repo.Name, err))
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sort.Strings(tags)
		for _, tag := range tags {
			if !repo.eligible(tag) {
				continue
			}
			digest, _, _, err := w.client.ManifestDigest(ctx, repo.Name, tag)
			if err != nil {
				w.emitError(fmt.Sprintf("manifest %s:%s: %v", repo.Name, tag, err))
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			key := stateKey(repo.Name, tag)
			w.mu.Lock()
			prev, seen := w.state[key]
			if !seen || prev != digest {
				w.state[key] = digest
			}
			w.mu.Unlock()
			if seen && prev == digest {
				continue
			}
			req := PublishRequest{Repo: repo.Name, Tag: tag, Digest: digest}
			emitted = append(emitted, req)
			w.onNotice(fmt.Sprintf("publish request %s:%s@%s", req.Repo, req.Tag, req.Digest))
		}
	}
	if len(emitted) > 0 {
		if err := w.persist(); err != nil {
			w.emitError(fmt.Sprintf("persist state: %v", err))
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return emitted, firstErr
}

// Run polls until ctx is done, sleeping poll between scans, with capped
// exponential backoff on scan errors.
func (w *Watcher) Run(ctx context.Context, trigger func(context.Context, PublishRequest) error) error {
	backoff := time.Duration(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		reqs, err := w.PollOnce(ctx)
		if err == nil {
			backoff = 0
			for _, req := range reqs {
				if trigger == nil {
					continue
				}
				if terr := trigger(ctx, req); terr != nil {
					w.emitError(fmt.Sprintf("trigger %s:%s: %v", req.Repo, req.Tag, terr))
				}
			}
		}
		sleep := w.cfg.Poll
		if err != nil {
			if backoff == 0 {
				backoff = w.cfg.Poll
			} else if backoff < 10*time.Minute {
				backoff *= 2
			}
			sleep = backoff
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(sleep):
		}
	}
}

func (w *Watcher) emitError(detail string) {
	if w.onEvent != nil {
		w.onEvent("watcher-error", detail)
	}
}
