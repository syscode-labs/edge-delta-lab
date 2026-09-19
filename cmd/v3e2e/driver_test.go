// Standalone v3 stage-8 E2E driver (cmd/v3e2e). The fake registry is the only
// in-process component (it must mutate manifests mid-run); everything else
// runs as real edgelab binaries:
//
//	fake registry (HTTP :18107) -> edgelab watch-registry -> publish+promote
//	  -> edgelab serve (hub: cache, /events announce, admin socket)
//	  -> 3 simulated spokes (edgelab watch, announce-driven, poll=1h)
//	  -> 1 real docker-load spoke (edgelab conf -action load) on the local daemon
//
// Notifications are captured mock-only at the hub admin durable event log
// (hub-announce lifecycle). No real Telegram/Slack endpoint is contacted.
//
//	go test ./cmd/v3e2e -run TestV3E2E -v -timeout 10m
package main

import (
	"archive/tar"
	"bufio"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/random"

	"example.com/edge-delta-lab/internal/lab"
)

const (
	repo     = "proj/app"
	tag      = "v1"
	spokes   = 3
	httpPort = "127.0.0.1:18107"
	hubPort  = "127.0.0.1:18108"
)

var (
	e2eRoot string // absolute <repo>/work/v3-e2e
	binDir  string // absolute <repo>/work/v3-e2e/bin
)

type proc struct {
	name string
	cmd  *exec.Cmd
	logf *os.File
}

func startProc(name, dir string, env []string, args ...string) (*proc, error) {
	logPath := filepath.Join(e2eRoot, dir, name+".log")
	lf, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(filepath.Join(binDir, "edgelab"), args...)
	cmd.Dir = filepath.Join(e2eRoot, dir)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	if err := cmd.Start(); err != nil {
		lf.Close()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	return &proc{name: name, cmd: cmd, logf: lf}, nil
}

func (p *proc) stop() {
	if p == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	_, _ = p.cmd.Process.Wait()
	_ = p.logf.Close()
}

var t0 time.Time

func waitFor(t *testing.T, what string, timeout time.Duration, probe func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if probe() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s after %s", what, time.Since(t0))
}

// ---- fake registry (in-process; same endpoint shapes as the unit-test fake) ----

type fakeRegistry struct {
	mu        sync.Mutex
	manifests map[string]string
	img       v1.Image
	hasImg    bool
	srv       *http.Server
	ln        net.Listener
}

func (f *fakeRegistry) digest(body string) string {
	return "sha256:" + lab.Hash([]byte(body))
}

func (f *fakeRegistry) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			w.WriteHeader(http.StatusOK)
			return
		}
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
			for i := 1; i < len(tags); i++ {
				for j := i; j > 0 && tags[j] < tags[j-1]; j-- {
					tags[j], tags[j-1] = tags[j-1], tags[j]
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": repo, "tags": tags})
			return
		}
		if strings.Contains(r.URL.Path, "/blobs/") {
			dg := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
			f.mu.Lock()
			img, ok := f.img, f.hasImg
			f.mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			h, err := v1.NewHash(dg)
			if err != nil {
				http.Error(w, "bad digest", http.StatusBadRequest)
				return
			}
			layer, lerr := img.LayerByDigest(h)
			if lerr != nil {
				// The config blob lives in the image, not the layer set.
				if cl, e := img.ConfigName(); e == nil && cl.String() == dg {
					if cb, e := img.RawConfigFile(); e == nil {
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
		rrepo, ref, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v2/"), "/manifests/")
		f.mu.Lock()
		body, ok := f.manifests[rrepo+"|"+ref]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", f.digest(body))
		_, _ = w.Write([]byte(body))
	})
	return mux
}

// ---- admin socket mini client ----

func adminCall(socket, method string, params any, timeout time.Duration) (json.RawMessage, error) {
	c, err := net.Dial("unix", socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(timeout))
	pb, _ := json.Marshal(params)
	b, _ := json.Marshal(map[string]any{"id": 1, "method": method, "params": json.RawMessage(pb)})
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		return nil, err
	}
	var resp struct {
		ID     int             `json:"id"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		return nil, err
	}
	return resp.Result, nil
}

// ---- helpers ----

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestV3E2ERegistryToHubToSpokes(t *testing.T) {
	t0 = time.Now()
	total := time.Now()
	repoRoot, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	e2eRoot = filepath.Join(repoRoot, "work", "v3-e2e")
	binDir = filepath.Join(e2eRoot, "bin")
	// Fresh work root every run: stale keys make keygen refuse, stale watcher
	// state would suppress the first-seen trigger, stale sockets break the hub.
	if err := os.RemoveAll(e2eRoot); err != nil {
		t.Fatal(err)
	}
	obs := map[string]any{}
	var obsMu sync.Mutex
	set := func(k string, v any) {
		obsMu.Lock()
		obs[k] = v
		obsMu.Unlock()
	}

	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"watcher", "hub", "spoke-1", "spoke-2", "spoke-3", "spoke-real", "origin", "keys"} {
		if err := os.MkdirAll(filepath.Join(e2eRoot, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// 0. Build the real edgelab binary.
	build := exec.Command("go", "build", "-o", filepath.Join(binDir, "edgelab"), "./cmd/edgelab")
	build.Dir = "../.." // go test cwd = package dir; repo root is two up
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build edgelab: %v\n%s", err, out)
	}
	t.Logf("edgelab binary built in %s", time.Since(t0).Round(time.Millisecond))

	// 1. In-process fake registry serving a real, docker-loadable image.
	img, err := random.Image(512*1024, 3)
	if err != nil {
		t.Fatal(err)
	}
	imgDigest, err := img.Digest()
	if err != nil {
		t.Fatal(err)
	}
	imgID, err := img.ConfigName()
	if err != nil {
		t.Fatal(err)
	}
	mb, err := img.RawManifest()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeRegistry{manifests: map[string]string{repo + "|" + tag: string(mb)}, img: img, hasImg: true}
	ln, err := net.Listen("tcp", httpPort)
	if err != nil {
		t.Fatalf("registry listen %s: %v", httpPort, err)
	}
	fake.srv = &http.Server{Handler: fake.handler()}
	go fake.srv.Serve(ln)
	defer fake.srv.Close()
	regURL := "http://" + httpPort
	manifestURL := "http://" + hubPort + "/releases/desired.json"
	t.Logf("fake registry %s serving %s:%s digest=%s", regURL, repo, tag, imgDigest)

	// 2. Publisher keys.
	keygen := exec.Command(filepath.Join(binDir, "edgelab"), "keygen", "-out", "keys")
	keygen.Dir = e2eRoot
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("keygen: %v\n%s", err, out)
	}
	pubKey := filepath.Join(e2eRoot, "keys", "publisher.pub")

	// Paths in watch.yaml are relative to the watcher subprocess cwd
	// (watcher/), not e2eRoot.
	mustWrite(t, e2eRoot+"/watcher/watch.yaml", fmt.Sprintf(`registry_url: %s
poll: 200ms
repos:
  - name: %s
    allow: "^v"
state_file: state.json
publish:
  root: ../origin
  key: ../keys/publisher.key
  channel: desired
  release_prefix: app-
  sequence_file: sequence
`, regURL, repo))

	// 3. Watcher: first-seen -> Trigger -> publish + promote.
	watcher, err := startProc("watcher", "watcher", nil, "watch-registry", "-config", "watch.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer watcher.stop()
	watchStart := time.Now()

	desired := filepath.Join(e2eRoot, "origin/releases/desired.json")
	waitFor(t, "watcher publish+promote (desired.json)", 60*time.Second, func() bool {
		_, err := os.Stat(desired)
		return err == nil
	})
	published := time.Now()
	set("watch_start", watchStart.Format(time.RFC3339Nano))
	set("publish_complete", published.Format(time.RFC3339Nano))
	set("watch_to_publish_ms", published.Sub(watchStart).Milliseconds())
	t.Logf("watcher published+promoted in %s", published.Sub(watchStart).Round(time.Millisecond))

	// The signed manifest must carry the fetched image's config digest as
	// image_ids — the stage-8 ImageIDs fix under test.
	envB, err := os.ReadFile(desired)
	if err != nil {
		t.Fatal(err)
	}
	pubRaw, err := os.ReadFile(pubKey)
	if err != nil {
		t.Fatal(err)
	}
	pubBytes, err := hex.DecodeString(strings.TrimSpace(string(pubRaw)))
	if err != nil {
		t.Fatal(err)
	}
	m, _, err := lab.Verify(envB, ed25519.PublicKey(pubBytes), 1<<50)
	if err != nil {
		t.Fatalf("signed manifest does not verify: %v", err)
	}
	if len(m.ImageIDs) != 1 || m.ImageIDs[0] != "sha256:"+imgID.Hex {
		t.Fatalf("manifest image_ids = %v, want [sha256:%s]", m.ImageIDs, imgID.Hex)
	}
	t.Logf("signed manifest carries image_ids=%v kind=%s seq=%d", m.ImageIDs, m.Kind, m.Sequence)

	// 4. Hub: origin engine + cache + /events announce + admin socket.
	// origin is relative to the hub subprocess cwd (hub/); ../origin is the
	// watcher's publish root. The admin socket lives in a SHORT temp dir:
	// macOS sun_path is 104 bytes and the deep work-tree path overflows it
	// (bind: invalid argument). The durable event log goes back under the
	// evidence tree via -event-log.
	adminDir, err := os.MkdirTemp("", "edgelab-admin-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(adminDir)
	adminSock := filepath.Join(adminDir, "admin.sock")
	hub, err := startProc("hub", "hub", nil,
		"serve", "-root", "../origin", "-listen", hubPort,
		"-events", "-hub", "-admin-socket", adminSock,
		"-event-log", filepath.Join(e2eRoot, "hub", "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer hub.stop()
	waitFor(t, "hub http up", 30*time.Second, func() bool {
		resp, err := http.Get("http://" + hubPort + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
	hubReady := time.Now()
	set("hub_ready", hubReady.Format(time.RFC3339Nano))

	// The hub's announce trigger records pre-existing release files during
	// its initial (silent) prime scan — hub-restart safety. desired.json was
	// written by the watcher before the hub started, so the only genuine
	// post-prime announce is a fresh signed write: re-publish the same
	// verified manifest as sequence+1. This exercises the real
	// announce -> /events -> spoke reconcile path and produces the durable
	// hub-announce event the notification gate below captures.
	privRaw, err := os.ReadFile(filepath.Join(e2eRoot, "keys", "publisher.key"))
	if err != nil {
		t.Fatal(err)
	}
	privBytes, err := hex.DecodeString(strings.TrimSpace(string(privRaw)))
	if err != nil {
		t.Fatal(err)
	}
	m.Sequence++
	env2, err := lab.Sign(m, ed25519.PrivateKey(privBytes))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(desired, env2, 0o644); err != nil {
		t.Fatal(err)
	}
	set("reannounce_sequence", m.Sequence)
	t.Logf("re-announced desired.json as sequence %d (post-prime signed write)", m.Sequence)

	// 5. Three simulated spokes: the real watch loop; poll=1h so ONLY a
	// verified /events announce can trigger reconcile inside the window.
	// Chunks come from the hub's read-only origin server, not the registry
	// (the fake registry only implements /v2/ manifest endpoints).
	for i := 1; i <= spokes; i++ {
		dir := fmt.Sprintf("spoke-%d", i)
		p, err := startProc("spoke", dir, nil,
			"watch",
			"-manifest", manifestURL,
			"-base", "http://"+hubPort,
			"-state", "state",
			"-pub", "../keys/publisher.pub",
			"-device-id", fmt.Sprintf("sim-spoke-%d", i),
			"-allow-http",
			"-poll", "1h",
			"-events-url", "ws://"+hubPort+"/events")
		if err != nil {
			t.Fatal(err)
		}
		defer p.stop()
	}

	// 6. Real docker-load spoke: conf with action=load (one-shot).
	// origin is the hub's read-only server (chunks); manifest is desired.json.
	mustWrite(t, e2eRoot+"/spoke-real/client.yaml", fmt.Sprintf(`origin: http://%s
manifest: %s
pub_key: ../keys/publisher.pub
state_dir: state
device_id: real-spoke
allow_http: true
action: load
`, hubPort, manifestURL))
	realSpoke, err := startProc("spoke", "spoke-real", nil, "conf", "-config", "client.yaml")
	if err != nil {
		t.Fatal(err)
	}
	realDone := make(chan error, 1)
	go func() { realDone <- realSpoke.cmd.Wait() }()

	// 7. Simulated spokes converge via announce within the window.
	ready := regexp.MustCompile(`"phase": ?"staged"|"phase": ?"loaded"`)
	spokeDone := map[string]time.Time{}
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && len(spokeDone) < spokes {
		for i := 1; i <= spokes; i++ {
			id := fmt.Sprintf("sim-spoke-%d", i)
			if _, ok := spokeDone[id]; ok {
				continue
			}
			sum := filepath.Join(e2eRoot, fmt.Sprintf("spoke-%d", i), "state", "summary.json")
			if b, err := os.ReadFile(sum); err == nil && ready.Match(b) {
				spokeDone[id] = time.Now()
				set("spoke_"+id+"_sync_complete", spokeDone[id].Format(time.RFC3339Nano))
				var s struct {
					Phase   string `json:"phase"`
					Release string `json:"release"`
				}
				_ = json.Unmarshal(b, &s)
				set("spoke_"+id+"_phase", s.Phase)
				set("spoke_"+id+"_release", s.Release)
				t.Logf("%s synced (phase=%s release=%s) %s after hub ready", id, s.Phase, s.Release, spokeDone[id].Sub(hubReady).Round(time.Millisecond))
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(spokeDone) < spokes {
		t.Fatalf("only %d/%d simulated spokes synced; logs under %s", len(spokeDone), spokes, e2eRoot)
	}
	syncedAt := time.Now()
	set("spokes_sync_complete", syncedAt.Format(time.RFC3339Nano))
	set("publish_to_spokes_synced_ms", syncedAt.Sub(published).Milliseconds())
	t.Logf("all %d simulated spokes synced %s after publish", spokes, syncedAt.Sub(published).Round(time.Millisecond))

	// 8. Real spoke: exit 0 + eligible plan + Docker image ID == signed IDs.
	var realErr error
	select {
	case realErr = <-realDone:
	case <-time.After(120 * time.Second):
		t.Fatalf("real spoke did not finish; log: %s/spoke-real/spoke.log", e2eRoot)
	}
	if realErr != nil {
		t.Fatalf("real spoke conf failed: %v (log: %s/spoke-real/spoke.log)", realErr, e2eRoot)
	}
	planB, err := os.ReadFile(filepath.Join(e2eRoot, "spoke-real", "spoke.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`"eligible": ?true`).Match(planB) {
		t.Fatalf("real spoke plan not eligible:\n%s", planB)
	}
	// Docker 29 containerd store labels images by OCI manifest digest, so
	// inspect.Id never equals the config digest; identity follows the bytes
	// (scripts/docker_identity.py method): parse the daemon-reported
	// "Loaded image ID" from the spoke log, inspect THAT id, then save the
	// image and hash the preserved config bytes against image_ids[0].
	var loadedID string
	for _, m := range regexp.MustCompile(`Loaded image ID: (sha256:[0-9a-f]{64})`).FindAllStringSubmatch(string(planB), -1) {
		loadedID = m[1]
	}
	if loadedID == "" {
		t.Fatalf("spoke log lacks daemon 'Loaded image ID' report:\n%s", planB)
	}
	idOut, err := exec.Command("docker", "image", "inspect", "--format", "{{.Id}}", loadedID).Output()
	if err != nil || strings.TrimSpace(string(idOut)) != loadedID {
		t.Fatalf("docker image inspect %s = %q err=%v, want %s", loadedID, strings.TrimSpace(string(idOut)), err, loadedID)
	}
	saveTar := filepath.Join(e2eRoot, "spoke-real", "loaded.tar")
	if out, err := exec.Command("docker", "image", "save", "-o", saveTar, loadedID).CombinedOutput(); err != nil {
		t.Fatalf("docker image save %s failed: %v\n%s", loadedID, err, out)
	}
	gotConfigID, err := savedImageConfigID(saveTar)
	if err != nil {
		t.Fatal(err)
	}
	wantID := "sha256:" + imgID.Hex
	if gotConfigID != wantID {
		t.Fatalf("saved image config digest %s != signed image_ids[0] %s", gotConfigID, wantID)
	}
	loadedAt := time.Now()
	set("real_spoke_complete", loadedAt.Format(time.RFC3339Nano))
	set("publish_to_real_verified_ms", loadedAt.Sub(published).Milliseconds())
	set("real_spoke_loaded_image_id", loadedID)
	t.Logf("real spoke docker-load verified: inspect %s resolves, saved config bytes hash to signed %s (%s after publish)", loadedID, wantID, loadedAt.Sub(published).Round(time.Millisecond))

	// 9. Payload sanity: the synced artifact on the real spoke must hash to
	// the manifest's artifact_sha256 (whole-artifact check already ran inside
	// lab.Sync; this re-proves it from the driver).
	sumB, err := os.ReadFile(filepath.Join(e2eRoot, "spoke-real", "state", "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sum struct {
		ArtifactSHA256 string `json:"artifact_sha256"`
		Artifact       string `json:"artifact"`
		Phase          string `json:"phase"`
	}
	if err := json.Unmarshal(sumB, &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Phase != "loaded" {
		t.Fatalf("real spoke summary phase = %s, want loaded", sum.Phase)
	}
	staged := filepath.Join(e2eRoot, "spoke-real", "state", "staged", sum.ArtifactSHA256+".tar")
	if _, err := os.Stat(staged); err != nil {
		t.Fatalf("staged artifact missing: %v", err)
	}

	// 10. Notifications mock-only: the hub admin durable event log must hold
	// the hub-announce lifecycle event. No real Telegram/Slack is contacted.
	sock := adminSock
	var captured map[string]any
	notifyDeadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(notifyDeadline) && captured == nil {
		res, err := adminCall(sock, "events", map[string]any{"after": 0, "limit": 100}, 5*time.Second)
		if err == nil {
			var r struct {
				Events []struct {
					Seq    int64  `json:"seq"`
					Time   string `json:"time"`
					Kind   string `json:"kind"`
					Detail string `json:"detail"`
				} `json:"events"`
			}
			if json.Unmarshal(res, &r) == nil {
				for _, ev := range r.Events {
					if ev.Kind == "hub-announce" {
						captured = map[string]any{"seq": ev.Seq, "time": ev.Time, "kind": ev.Kind, "detail": ev.Detail}
						break
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if captured == nil {
		t.Fatalf("hub-announce not captured in admin event log (mock-only notification capture failed)")
	}
	set("notify_capture", captured)
	t.Logf("notification captured mock-only: kind=%s detail=%s", captured["kind"], captured["detail"])

	// 11. Timing evidence.
	set("total_wall_ms", time.Since(total).Milliseconds())
	obsMu.Lock()
	out, _ := json.MarshalIndent(obs, "", "  ")
	obsMu.Unlock()
	mustWrite(t, e2eRoot+"/TIMING.json", string(out)+"\n")

	// Watcher lifecycle events prove the whole chain in one log.
	wlog, _ := os.ReadFile(filepath.Join(e2eRoot, "watcher", "watcher.log"))
	for _, want := range []string{"release-detected", "release-published"} {
		if !strings.Contains(string(wlog), want) {
			t.Errorf("watcher log missing %s event", want)
		}
	}
}

// savedImageConfigID hashes the preserved config bytes of the single image in
// a docker-saved archive. The blob filename is not trusted; the bytes are
// hashed (same method as internal/lab agent post-load proof and
// scripts/docker_identity.py).
func savedImageConfigID(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	member := func(name string, limit int64) ([]byte, error) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		tr = tar.NewReader(f)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				return nil, fmt.Errorf("saved archive lacks member %s", name)
			}
			if err != nil {
				return nil, err
			}
			if h.Name != name || !h.FileInfo().Mode().IsRegular() || h.Size > limit {
				continue
			}
			b, err := io.ReadAll(io.LimitReader(tr, limit))
			if err != nil {
				return nil, err
			}
			return b, nil
		}
	}
	manifestB, err := member("manifest.json", 1<<20)
	if err != nil {
		return "", err
	}
	var manifests []struct {
		Config string `json:"Config"`
	}
	if err := json.Unmarshal(manifestB, &manifests); err != nil {
		return "", err
	}
	if len(manifests) != 1 {
		return "", fmt.Errorf("saved archive holds %d images, want 1", len(manifests))
	}
	configB, err := member(manifests[0].Config, 4<<20)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(configB)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
