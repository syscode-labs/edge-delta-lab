package push

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"example.com/edge-delta-lab/internal/lab"
)

// pushRig is a hub-shaped lab: origin root, publisher keys, a broker behind a
// FaultServer /events endpoint, and small-CDC publish helpers.
type pushRig struct {
	t       *testing.T
	dir     string
	root    string
	key     string
	pub     ed25519.PublicKey
	broker  *Broker
	srv     *lab.FaultServer
	http    *httptest.Server
	wsURL   string
	// trigger is started lazily via startTrigger() so tests can seed
	// releases BEFORE the initial scan: the trigger is write-only by
	// contract, but files present at startup must not announce (hub
	// restart safety), so seeding after start would be legitimate and
	// defeat the tests that assert initial silence.
	trigger *Trigger
	started bool
	startMu sync.Mutex
}

func newPushRig(t *testing.T, poll time.Duration) *pushRig {
	t.Helper()
	d := t.TempDir()
	keys := filepath.Join(d, "keys")
	if e := lab.Keygen(keys); e != nil {
		t.Fatal(e)
	}
	pub, e := lab.ReadKey(filepath.Join(keys, "publisher.pub"), ed25519.PublicKeySize)
	if e != nil {
		t.Fatal(e)
	}
	r := &pushRig{
		t:    t,
		dir:  d,
		root: filepath.Join(d, "origin"),
		key:  filepath.Join(keys, "publisher.key"),
		pub:  ed25519.PublicKey(pub),
	}
	r.broker = NewBroker()
	r.srv = lab.NewFaultServer(r.root, lab.FaultPlan{})
	r.srv.Events = r.broker.Handler()
	r.http = httptest.NewServer(r.srv)
	t.Cleanup(r.http.Close)
	r.wsURL = "ws" + strings.TrimPrefix(r.http.URL, "http") + "/events"
	r.trigger = NewTrigger(TriggerOptions{Root: r.root, Broker: r.broker, Channels: []string{"desired"}, Poll: poll})
	// NOT started here: tests call startTrigger() after seeding so the
	// initial ScanOnce records stamps for the pre-existing files silently.
	return r
}

// startTrigger begins the trigger loop exactly once (guarded so the barrier
// helpers stay race-free under -race) and registers its stop with cleanup.
func (r *pushRig) startTrigger() {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	if r.started {
		return
	}
	r.started = true
	ctx, cancel := context.WithCancel(context.Background())
	r.t.Cleanup(cancel)
	go r.trigger.Run(ctx)
}

func (r *pushRig) publish(t *testing.T, name string, seq uint64, b []byte) lab.Manifest {
	t.Helper()
	in := filepath.Join(r.dir, name+".tar")
	if e := os.WriteFile(in, b, 0600); e != nil {
		t.Fatal(e)
	}
	m, e := lab.Publish(lab.PublishOptions{Input: in, Root: r.root, Key: r.key, Release: name, Sequence: seq, Kind: "raw", Min: 1024, Avg: 4096, Max: 16384})
	if e != nil {
		t.Fatal(e)
	}
	return m
}

// promote copies a signed release envelope onto the desired channel the same
// way `edgelab promote` does (an atomic channel-file WRITE).
func (r *pushRig) promote(t *testing.T, release string) {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(r.root, "releases", release+".json"))
	if e != nil {
		t.Fatal(e)
	}
	if e := lab.AtomicWrite(filepath.Join(r.root, "releases", "desired.json"), b, 0644); e != nil {
		t.Fatal(e)
	}
}

func (r *pushRig) syncOptions() lab.AgentOptions {
	o := lab.DefaultAgentOptions()
	o.StateDir = filepath.Join(r.dir, "edge")
	o.PublicKey = filepath.Join(r.dir, "keys", "publisher.pub")
	o.ManifestURL = r.http.URL + "/releases/desired.json"
	o.BaseURL = r.http.URL
	o.AllowHTTP = true
	o.Backoff = time.Millisecond
	o.MaxBackoff = 5 * time.Millisecond
	o.RequestTimeout = 2 * time.Second
	o.ReserveBytes = 0
	return o
}

func waitFor(t *testing.T, d time.Duration, what string, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func payload(n int, seed byte) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = seed + byte(i%251)
	}
	return b
}

// The /events endpoint must refuse plain HTTP loudly (misconfigured pollers
// should see 426, not silence).
func TestEventsHandlerRejectsPlainHTTP(t *testing.T) {
	r := newPushRig(t, 10*time.Millisecond)
	resp, err := http.Get(r.http.URL + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("want 426, got %d", resp.StatusCode)
	}
}

// Announcements fire on channel-file WRITES only: the initial scan, unchanged
// re-stats and pure reads stay silent; a same-content rewrite does not
// re-announce (digest dedupe).
func TestTriggerAnnouncesWritesOnly(t *testing.T) {
	r := newPushRig(t, 10*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ch, cf := r.broker.Subscribe(ctx)
	defer cf()

	// Seed release v1 + channel BEFORE the trigger has ever seen them:
	// files present at the initial scan must not announce (hub restart
	// safety), so the trigger is started only after seeding.
	r.publish(t, "v1", 1, payload(64<<10, 1))
	r.promote(t, "v1")
	r.startTrigger()

	// The trigger's initial ScanOnce must not announce the pre-existing files.
	waitFor(t, 2*time.Second, "initial scan to record stamps", func() bool {
		select {
		case <-ch:
			return false // any announcement here is the read-trigger bug again
		default:
		}
		return r.trigger.SeenFiles() == 2
	})
	if st := r.broker.Snapshot(); st.Announced != 0 {
		t.Fatalf("initial scan announced: %+v", st)
	}

	// Pure reads never announce.
	if _, e := os.ReadFile(filepath.Join(r.root, "releases", "desired.json")); e != nil {
		t.Fatal(e)
	}
	time.Sleep(50 * time.Millisecond)
	if st := r.broker.Snapshot(); st.Announced != 0 {
		t.Fatalf("read triggered an announcement: %+v", st)
	}

	// A publish (new immutable file) announces kind=publish.
	r.publish(t, "v2", 2, payload(64<<10, 2))
	select {
	case a := <-ch:
		if a.Kind != "publish" || a.Release != "v2" || a.Sequence != 2 {
			t.Fatalf("publish announce: %+v", a)
		}
		if a.Digest != lab.Hash(a.Envelope) {
			t.Fatal("digest is not sha256 of envelope bytes")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no publish announce")
	}

	// A promote (channel write) announces kind=promote.
	r.promote(t, "v2")
	select {
	case a := <-ch:
		if a.Kind != "promote" || a.Release != "v2" || a.Sequence != 2 {
			t.Fatalf("promote announce: %+v", a)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no promote announce")
	}

	// Rewriting the channel with the SAME bytes is not a new release.
	r.promote(t, "v2")
	time.Sleep(100 * time.Millisecond)
	st := r.broker.Snapshot()
	if st.Announced != 2 {
		t.Fatalf("same-content rewrite re-announced: %+v", st)
	}
}

// 4.4: publish → connected client syncs within one interval without polling.
// The client holds one websocket, receives the verified announce, and
// reconciles immediately; the poll timer never fires and desired.json is
// fetched exactly twice (initial sync + announce-triggered reconcile).
func TestPublishAnnouncesConnectedClientSyncsWithoutPolling(t *testing.T) {
	r := newPushRig(t, 10*time.Millisecond)
	r.publish(t, "v1", 1, payload(64<<10, 1))
	r.promote(t, "v1")
	r.startTrigger()

	s1, e := lab.Sync(context.Background(), r.syncOptions())
	if e != nil || s1.Release != "v1" || s1.Sequence != 1 {
		t.Fatalf("initial sync: %+v %v", s1, e)
	}

	kick := make(chan lab.Manifest, 1)
	// Barrier: wait for the websocket handshake to be visible on the hub side
	// (broker subscriber count) before publishing, so the announce cannot win
	// a race against the dial. Snapshot is mutex-guarded, so this is race-free.
	go Run(context.Background(), ClientOptions{
		URL:    r.wsURL,
		Device: "edge-01",
		OnAnnounce: func(a Announce) {
			m, err := Accept(a, r.pub, 10<<30)
			if err != nil {
				return // hints never bypass verification
			}
			select {
			case kick <- m:
			default:
			}
		},
	})
	waitFor(t, 3*time.Second, "websocket handshake", func() bool {
		return r.broker.Snapshot().Subscribers == 1
	})

	published := time.Now()
	r.publish(t, "v2", 2, payload(96<<10, 2))
	r.promote(t, "v2")

	// The announce-driven reconcile must land far inside one 10s poll window.
	// Both the v2 publish AND the v2 promote announce: 2 total.
	select {
	case m := <-kick:
		if m.Release != "v2" || m.Sequence != 2 {
			t.Fatalf("announced manifest: %+v", m)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("connected client never received a usable announce")
	}
	if d := time.Since(published); d > 2*time.Second {
		t.Fatalf("announce path too slow for one interval: %s", d)
	}

	s2, e := lab.Sync(context.Background(), r.syncOptions())
	if e != nil {
		t.Fatal(e)
	}
	if s2.Release != "v2" || s2.Sequence != 2 {
		t.Fatalf("reconcile did not converge on v2: %+v", s2)
	}
	// Both v2 announcements fired (publish + promote) on the hub side.
	if st := r.broker.Snapshot(); st.Announced != 2 {
		t.Fatalf("broker: %+v", st)
	}
	// All served metadata bytes across both syncs equal exactly the two
	// distinct channel envelopes: no hidden poll fetched anything extra.
	if n := r.srv.Snapshot().MetadataRequests; n != 2 {
		t.Fatalf("desired.json fetched %d times, want exactly 2 (no polling)", n)
	}
}

// 4.5: forged/unknown announcements cause no state change. A wrong-key
// signature fails verification; a stale digest fails integrity; and even a
// VALIDLY signed announce for a release the signed channel does not name
// changes nothing — the channel reconcile remains the only source of truth.
func TestForgedAndUnknownAnnouncesChangeNothing(t *testing.T) {
	r := newPushRig(t, 10*time.Millisecond)
	r.publish(t, "v1", 1, payload(64<<10, 1))
	r.promote(t, "v1")
	o := r.syncOptions()
	s1, e := lab.Sync(context.Background(), o)
	if e != nil || s1.Release != "v1" {
		t.Fatalf("initial sync: %+v %v", s1, e)
	}
	stateBefore, e := os.ReadFile(filepath.Join(o.StateDir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}

	// Second key = forger.
	other := filepath.Join(t.TempDir(), "keys")
	if e := lab.Keygen(other); e != nil {
		t.Fatal(e)
	}

	// A v2 release signed by the REAL key, never promoted: the "unknown"
	// release the signed channel does not name.
	r.publish(t, "v2-shadow", 2, payload(64<<10, 3))
	shadowEnv, e := os.ReadFile(filepath.Join(r.root, "releases", "v2-shadow.json"))
	if e != nil {
		t.Fatal(e)
	}
	// Forge: same bytes, signed by the WRONG key.
	forged, e := lab.Sign(lab.Manifest{Version: lab.FormatVersion, Release: "evil", Sequence: 9, Kind: "raw",
		ArtifactSHA256: strings.Repeat("a", 64), ArtifactSize: 1, Chunker: "gear64-lab-v1",
		Min: 1, Avg: 2, Max: 3}, mustReadPriv(t, filepath.Join(other, "publisher.key")))
	if e != nil {
		t.Fatal(e)
	}
	// Tampered: valid v2 envelope with one payload byte flipped (digest left stale).
	var env lab.Envelope
	if e := json.Unmarshal(shadowEnv, &env); e != nil {
		t.Fatal(e)
	}
	tampered, e := lab.Sign(lab.Manifest{Version: lab.FormatVersion, Release: "v2-shadow", Sequence: 2, Kind: "raw",
		ArtifactSHA256: strings.Repeat("b", 64), ArtifactSize: 1, Chunker: "gear64-lab-v1",
		Min: 1, Avg: 2, Max: 3}, mustReadPriv(t, r.key))
	if e != nil {
		t.Fatal(e)
	}
	tampered = append([]byte{}, tampered...)
	tampered[len(tampered)/2] ^= 0x20

	syncs := make(chan string, 8)
	go Run(context.Background(), ClientOptions{
		URL:    r.wsURL,
		Device: "edge-01",
		OnAnnounce: func(a Announce) {
			if _, err := Accept(a, r.pub, 10<<30); err != nil {
				return // forged/tampered: reject, no reconcile
			}
			syncs <- "verified"
		},
	})
	// Barrier: dial completes before announcements are injected.
	waitFor(t, 3*time.Second, "websocket handshake", func() bool {
		return r.broker.Snapshot().Subscribers == 1
	})

	bad := []Announce{
		{Kind: "promote", Release: "evil", Sequence: 9, Digest: lab.Hash(forged), Envelope: forged},
		{Kind: "promote", Release: "v2-shadow", Sequence: 2, Digest: lab.Hash(shadowEnv), Envelope: tampered},
		// Empty envelope and stale digest variants.
		{Kind: "promote", Release: "v2-shadow", Sequence: 2, Envelope: shadowEnv},
	}
	for _, a := range bad {
		r.broker.Announce(a) // as if delivered by a malicious hub
	}

	// The one legitimate announce (valid signature, unknown to the channel)
	// verifies but must not, by itself, change device state: the reconcile
	// against desired.json still resolves to v1.
	r.broker.Announce(Announce{Kind: "promote", Release: "v2-shadow", Sequence: 2, Digest: lab.Hash(shadowEnv), Envelope: shadowEnv})
	select {
	case <-syncs:
	case <-time.After(3 * time.Second):
		t.Fatal("validly signed announce was not verified")
	}
	s2, e := lab.Sync(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if s2.Release != "v1" || s2.Sequence != 1 {
		t.Fatalf("state moved without signed channel agreement: %+v", s2)
	}
	stateAfter, e := os.ReadFile(filepath.Join(o.StateDir, "state.json"))
	if e != nil {
		t.Fatal(e)
	}
	// state.json carries an `updated` wall-clock stamp that legitimately
	// changes on every Sync run; compare semantics with that field removed.
	if a, b := stripUpdated(t, stateBefore), stripUpdated(t, stateAfter); a != b {
		t.Fatalf("state changed from forged announces:\nbefore=%s\nafter=%s", a, b)
	}
}

// stripUpdated removes the volatile `updated` timestamp so state comparisons
// test substance (release, phase, artifact, accepted sequence/hash), not time.
func stripUpdated(t *testing.T, raw []byte) string {
	t.Helper()
	var m map[string]any
	if e := json.Unmarshal(raw, &m); e != nil {
		t.Fatalf("state.json is not an object: %v", e)
	}
	delete(m, "updated")
	out, e := json.Marshal(m) // map keys marshal in sorted order: stable
	if e != nil {
		t.Fatal(e)
	}
	return string(out)
}

func mustReadPriv(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	b, e := lab.ReadKey(path, ed25519.PrivateKeySize)
	if e != nil {
		t.Fatal(e)
	}
	return ed25519.PrivateKey(b)
}
