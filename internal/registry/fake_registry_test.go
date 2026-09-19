package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"example.com/edge-delta-lab/internal/lab"
)

// --- fake registry -------------------------------------------------------

type fakeRegistry struct {
	mu        sync.Mutex
	manifests map[string]string // repo|tag -> manifest body
	img       v1.Image          // optional backing image for blob serving
	repo      string            // repo the backing image belongs to
	watchAuth bool              // require bearer token on /v2/ subpaths
	tokensIssued,
	tokensChecked int
	realm *httptest.Server
	srv   *httptest.Server
}

func (f *fakeRegistry) digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (f *fakeRegistry) setManifest(repo, tag, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.manifests[repo+"|"+tag] = body
}

func (f *fakeRegistry) start(withAuth bool) *httptest.Server {
	f.mu.Lock()
	f.watchAuth = withAuth
	f.mu.Unlock()
	// Token realm endpoint.
	realm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("service") != "edgelab-test" {
			http.Error(w, "missing service", http.StatusBadRequest)
			return
		}
		if user, pass, ok := r.BasicAuth(); ok && user == "bot" && pass != "secret" {
			http.Error(w, "bad credentials", http.StatusUnauthorized)
			return
		}
		f.mu.Lock()
		f.tokensIssued++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"token": "tok-" + fmt.Sprint(time.Now().UnixNano()), "expires_in": 300})
	}))
	f.mu.Lock()
	f.realm = realm
	f.mu.Unlock()
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		needAuth := f.watchAuth
		if needAuth {
			f.tokensChecked++
		}
		f.mu.Unlock()
		if needAuth && !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="edgelab-test"`, realm.URL))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
		// /v2/<repo>/tags/list
		if strings.HasSuffix(r.URL.Path, "/tags/list") {
			repo := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/v2/"), "/tags/list")
			f.mu.Lock()
			var tags []string
			for k := range f.manifests {
				if p := strings.SplitN(k, "|", 2); p[0] == repo {
					tags = append(tags, p[1])
				}
			}
			f.mu.Unlock()
			sortStrings(tags)
			n := r.URL.Query().Get("n")
			last := r.URL.Query().Get("last")
			if n != "" {
				var limit int
				_, _ = fmt.Sscanf(n, "%d", &limit)
				start := 0
				if last != "" {
					for i, t := range tags {
						if t == last {
							start = i + 1
							break
						}
					}
				}
				end := start + limit
				if end > len(tags) {
					end = len(tags)
				}
				tags = tags[start:end]
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
			return
		}
		// /v2/<repo>/blobs/<digest>
		if strings.Contains(r.URL.Path, "/blobs/") {
			rest := strings.TrimPrefix(r.URL.Path, "/v2/")
			repo := rest[:strings.Index(rest, "/blobs/")]
			img := f.imageFor(repo)
			if img == nil {
				http.NotFound(w, r)
				return
			}
			dg := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			h, err := v1.NewHash(dg)
			if err != nil {
				http.Error(w, "bad digest", http.StatusBadRequest)
				return
			}
			layer, lerr := img.LayerByDigest(h)
			if lerr != nil {
				// The config blob lives in the image, not the layer set.
				if cl, e := img.ConfigName(); e == nil && cl.String() == dg {
					cb, e := img.RawConfigFile()
					if e == nil {
						w.Header().Set("Content-Type", "application/octet-stream")
						_, _ = w.Write(cb)
						return
					}
				}
				http.Error(w, "blob not found", http.StatusNotFound)
				return
			}
			rc, lerr2 := layer.Compressed()
			if lerr2 != nil {
				http.Error(w, "blob read failed", http.StatusInternalServerError)
				return
			}
			defer rc.Close()
			w.Header().Set("Content-Type", "application/octet-stream")
			_, _ = io.Copy(w, rc)
			return
		}
		// /v2/<repo>/manifests/<ref>
		repo, ref, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/")
		f.mu.Lock()
		body, ok := f.manifests[repo+"|"+ref]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", f.digest(body))
		_, _ = w.Write([]byte(body))
	})
	srv := httptest.NewServer(mux)
	f.srv = srv
	return srv
}

func (f *fakeRegistry) counts() (issued, checked int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.tokensIssued, f.tokensChecked
}

// serveImage backs the fake with a real v1.Image so blob endpoints serve
// config and layers for docker-archive export via go-containerregistry.
func (f *fakeRegistry) serveImage(repo string, img v1.Image) {
	f.mu.Lock()
	f.img = img
	f.repo = repo
	f.mu.Unlock()
}

func (f *fakeRegistry) imageFor(repo string) v1.Image {
	f.mu.Lock()
	defer f.mu.Unlock()
	if repo == f.repo {
		return f.img
	}
	return nil
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- client tests ---------------------------------------------------------

func TestClientTagsPaginationAndAuth(t *testing.T) {
	f := &fakeRegistry{manifests: map[string]string{}}
	for i := 0; i < 5; i++ {
		f.setManifest("proj/app", fmt.Sprintf("v1.%d", i), fmt.Sprintf(`{"tag":"v1.%d"}`, i))
	}
	srv := f.start(true)
	defer srv.Close()
	defer f.realm.Close()

	c, err := NewClient(srv.URL, AuthConfig{Username: "bot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, _, err := c.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	c.limit = 2 // force pagination
	tags, err := c.Tags(ctx, "proj/app")
	if err != nil {
		t.Fatalf("tags: %v", err)
	}
	if len(tags) != 5 {
		t.Fatalf("want 5 tags, got %v", tags)
	}
	if issued, checked := f.counts(); issued == 0 || checked < 2 {
		t.Fatalf("token issued=%d checked=%d", issued, checked)
	}
}

func TestClientManifestDigestHeader(t *testing.T) {
	f := &fakeRegistry{manifests: map[string]string{}}
	f.setManifest("proj/app", "v1", `{"layers":[]}`)
	srv := f.start(false)
	defer srv.Close()
	c, _ := NewClient(srv.URL, AuthConfig{})
	digest, body, ct, err := c.ManifestDigest(context.Background(), "proj/app", "v1")
	if err != nil {
		t.Fatal(err)
	}
	if digest != f.digest(`{"layers":[]}`) {
		t.Fatalf("digest mismatch: %s", digest)
	}
	if string(body) != `{"layers":[]}` || ct == "" {
		t.Fatalf("body/ct: %q %q", body, ct)
	}
}

// --- watcher tests --------------------------------------------------------

func testCfg(t *testing.T, baseURL string, repos []RepoConfig) (Config, string) {
	t.Helper()
	dir := t.TempDir()
	keyDir := filepath.Join(dir, "keys")
	if err := lab.Keygen(keyDir); err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		RegistryURL: baseURL,
		Poll:        10 * time.Millisecond,
		Repos:       repos,
		StateFile:   filepath.Join(dir, "state.json"),
		Publish: PublishConfig{
			Root:         filepath.Join(dir, "origin"),
			Key:          filepath.Join(keyDir, "publisher.key"),
			Channel:      "desired",
			SequenceFile: filepath.Join(dir, "sequence"),
		},
	}
	return cfg, dir
}

func TestWatcherFirstSeenDigestChangeAndFilters(t *testing.T) {
	f := &fakeRegistry{manifests: map[string]string{}}
	f.setManifest("proj/app", "v1", `{"a":1}`)
	f.setManifest("proj/app", "v2", `{"b":2}`)
	f.setManifest("proj/app", "v2-canary", `{"c":3}`) // ignored
	f.setManifest("other/thing", "v9", `{"d":4}`)     // other repo not watched
	srv := f.start(false)
	defer srv.Close()

	cfg, _ := testCfg(t, srv.URL, []RepoConfig{{Name: "proj/app", Allow: "^v", Ignore: "canary$"}})
	var mu sync.Mutex
	var reqs []PublishRequest
	w, err := NewWatcher(mustClient(t, srv.URL), cfg, WithEventSink(func(kind, detail string) {
		t.Errorf("unexpected event %s: %s", kind, detail)
	}))
	if err != nil {
		t.Fatal(err)
	}
	reqs, err = w.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("want v1+v2 first-seen, got %+v", reqs)
	}
	// Unchanged poll: nothing new.
	reqs, err = w.PollOnce(context.Background())
	if err != nil || len(reqs) != 0 {
		t.Fatalf("second poll should be empty: %v %v", reqs, err)
	}
	// Digest change on v2, new tag v3, ignored canary change stays ignored.
	f.setManifest("proj/app", "v2", `{"b":22}`)
	f.setManifest("proj/app", "v3", `{"e":5}`)
	f.setManifest("proj/app", "v2-canary", `{"c":33}`)
	mu.Lock()
	reqs, err = w.PollOnce(context.Background())
	mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 2 {
		t.Fatalf("want v2-change+v3, got %+v", reqs)
	}
	seen := map[string]bool{}
	for _, r := range reqs {
		seen[r.Tag] = true
	}
	if !seen["v2"] || !seen["v3"] {
		t.Fatalf("missing expected tags: %v", seen)
	}
}

func TestWatcherRestartPersistsState(t *testing.T) {
	f := &fakeRegistry{manifests: map[string]string{}}
	f.setManifest("proj/app", "v1", `{"a":1}`)
	srv := f.start(false)
	defer srv.Close()

	cfg, _ := testCfg(t, srv.URL, []RepoConfig{{Name: "proj/app"}})
	w, err := NewWatcher(mustClient(t, srv.URL), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if reqs, _ := w.PollOnce(context.Background()); len(reqs) != 1 {
		t.Fatalf("first-seen: %+v", reqs)
	}
	// A fresh watcher on the same state file must not re-emit.
	w2, err := NewWatcher(mustClient(t, srv.URL), cfg)
	if err != nil {
		t.Fatal(err)
	}
	reqs, err := w2.PollOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 0 {
		t.Fatalf("restart re-emitted: %+v", reqs)
	}
}

func TestWatcherErrorEventsAndBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg, _ := testCfg(t, srv.URL, []RepoConfig{{Name: "proj/app"}})
	events := make(chan string, 8)
	w, err := NewWatcher(mustClient(t, srv.URL), cfg, WithEventSink(func(kind, detail string) {
		if kind == "watcher-error" {
			events <- detail
		}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PollOnce(context.Background()); err == nil {
		t.Fatal("expected poll error")
	}
	select {
	case d := <-events:
		if !strings.Contains(d, "tags/list") {
			t.Fatalf("unexpected event detail: %s", d)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no watcher-error event")
	}
}

func TestWatcherConfigValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "watch.yaml")
	cfg := "registry_url: http://x\nrepos:\n  - name: r\n    allow: \"(\"\nstate_file: s\npublish:\n  root: r\n  key: k\n  channel: c\n  sequence_file: q\n"
	if err := os.WriteFile(path, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("expected invalid regex to fail")
	}
}

// mustClient builds an unauthenticated client for tests.
func mustClient(t *testing.T, base string) *Client {
	t.Helper()
	c, err := NewClient(base, AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
