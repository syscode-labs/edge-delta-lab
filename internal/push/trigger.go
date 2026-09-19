package push

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"example.com/edge-delta-lab/internal/lab"
)

// TriggerOptions configures the release-file announce trigger.
type TriggerOptions struct {
	// Root is the published origin directory; <root>/releases/*.json is scanned.
	Root string
	// Poll is the stat-poll interval (default 250ms). The stdlib-only stat loop
	// deliberately avoids fsnotify-style dependencies in the lab.
	Poll time.Duration
	// Channels names mutable channel files (e.g. "desired"). Writes to those
	// files announce Kind "promote"; writes to any other releases/*.json
	// announce Kind "publish". Announcement fire is WRITE-ONLY: the initial
	// scan records state without announcing, and reads never announce.
	Channels []string
	// Broker receives announcements.
	Broker *Broker
	// Event optionally receives human-readable trigger events.
	Event func(kind, detail string)
}

// Trigger watches <root>/releases/*.json for WRITES via a stdlib stat-poll
// loop. On a changed file it reads the signed envelope bytes, parses the inner
// Manifest (base64-decode Envelope.Payload, then Release/Sequence), and hands
// the exact bytes to the broker with Digest = lab.Hash(envelopeBytes). Spokes
// re-verify every announcement; the trigger exists only to make hints fast.
type Trigger struct {
	opts TriggerOptions
	// stamps is the last observed write signature per release file. Guarded
	// by mu so tests can observe scan state race-free.
	mu    sync.Mutex
	stamps map[string]fileStamp
	// announced dedupes by content digest per path so a re-stat of unchanged
	// bytes (touch, promote of the same release) does not re-announce.
	announced map[string]string
	// primed turns true once the initial scan has completed; files that
	// appear afterwards are genuine publishes and must announce, while files
	// present before the first scan must not (hub-restart safety).
	primed bool
}

type fileStamp struct {
	size  int64
	mtime time.Time
	valid bool // false = file missing
}

// NewTrigger builds a trigger. Call Run in a goroutine.
func NewTrigger(o TriggerOptions) *Trigger {
	if o.Poll <= 0 {
		o.Poll = 250 * time.Millisecond
	}
	return &Trigger{opts: o, stamps: map[string]fileStamp{}, announced: map[string]string{}}
}

func (t *Trigger) event(kind, detail string) {
	if t.opts.Event != nil {
		t.opts.Event(kind, detail)
	}
}

// Run scans until ctx ends. Errors are reported via the event sink and never
// stop the loop: a missing or unreadable origin must not kill the hub.
func (t *Trigger) Run(ctx context.Context) {
	t.ScanOnce() // initial state: record stamps, announce nothing
	tick := time.NewTicker(t.opts.Poll)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			t.ScanOnce()
		}
	}
}

// SeenFiles returns how many release files the trigger has recorded stamps
// for (race-free observation for tests and the admin socket).
func (t *Trigger) SeenFiles() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.stamps)
}

// ScanOnce performs one scan: stat every releases/*.json, and for any file
// whose write signature changed since the last scan, read + announce it.
// Exported for tests.
func (t *Trigger) ScanOnce() {
	dir := filepath.Join(t.opts.Root, "releases")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			t.event("trigger_error", err.Error())
		}
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		path := filepath.Join(dir, name)
		st, err := os.Stat(path)
		if err != nil {
			continue
		}
		cur := fileStamp{size: st.Size(), mtime: st.ModTime(), valid: true}
		t.mu.Lock()
		prev, seen := t.stamps[name]
		t.stamps[name] = cur
		primed := t.primed
		t.mu.Unlock()
		// Announce on WRITES only: a changed write signature on a known file
		// (e.g. the channel being promoted), or a file that APPEARED after the
		// initial scan (a fresh publish). The initial scan itself stays silent.
		if (seen && cur != prev) || (!seen && primed) {
			t.announceFile(name, path)
		}
	}
	t.mu.Lock()
	t.primed = true
	t.mu.Unlock()
}

// announceFile reads the envelope bytes for one changed release file and
// pushes one announcement. A same-content rewrite is a no-op (digest dedupe).
func (t *Trigger) announceFile(name, path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		t.event("trigger_error", fmt.Sprintf("read %s: %v", name, err))
		return
	}
	digest := lab.Hash(b)
	t.mu.Lock()
	if prev, ok := t.announced[name]; ok && prev == digest {
		t.mu.Unlock()
		return // same bytes rewritten: not a new release
	}
	t.announced[name] = digest
	t.mu.Unlock()
	var env lab.Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.event("trigger_error", fmt.Sprintf("parse %s: %v", name, err))
		return
	}
	payload, err := base64.StdEncoding.DecodeString(env.Payload)
	if err != nil {
		t.event("trigger_error", fmt.Sprintf("payload %s: %v", name, err))
		return
	}
	var m struct {
		Release  string `json:"release"`
		Sequence uint64 `json:"sequence"`
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		t.event("trigger_error", fmt.Sprintf("manifest %s: %v", name, err))
		return
	}
	kind := "publish"
	base := strings.TrimSuffix(name, ".json")
	for _, c := range t.opts.Channels {
		if base == c {
			kind = "promote"
			break
		}
	}
	a := Announce{Kind: kind, Release: m.Release, Sequence: m.Sequence, Digest: digest, Envelope: b}
	t.event("announce", fmt.Sprintf("kind=%s release=%s sequence=%d file=%s", a.Kind, a.Release, a.Sequence, name))
	t.opts.Broker.Announce(a)
}

// Accept is the spoke-side gate for one received announcement. It re-verifies
// the envelope bytes against the pinned publisher key — never trusting the
// transport, the announced kind, release or sequence — and returns the parsed
// manifest on success. A failed verification is always an error; callers must
// not change device state without the signed channel agreeing separately.
func Accept(a Announce, pub ed25519.PublicKey, maxArtifact int64) (lab.Manifest, error) {
	var m lab.Manifest
	if len(a.Envelope) == 0 {
		return m, fmt.Errorf("announce %s: empty envelope", a.Release)
	}
	if a.Digest != "" && lab.Hash(a.Envelope) != a.Digest {
		return m, fmt.Errorf("announce %s: envelope digest mismatch", a.Release)
	}
	m, _, err := lab.Verify(a.Envelope, pub, maxArtifact)
	if err != nil {
		return lab.Manifest{}, fmt.Errorf("announce %s: %w", a.Release, err)
	}
	return m, nil
}
