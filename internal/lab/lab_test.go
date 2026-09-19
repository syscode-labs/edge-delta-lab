package lab

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func data(n int) []byte { b := make([]byte, n); _, _ = (&fixtureRNG{x: 42}).Read(b); return b }
func chunkSet(t *testing.T, b []byte) map[string]int {
	t.Helper()
	out := map[string]int{}
	e := Split(bytes.NewReader(b), 1024, 4096, 16384, func(c []byte) error { out[Hash(c)] = len(c); return nil })
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestCDCDeterministicAndBounded(t *testing.T) {
	b := data(1 << 20)
	var one, two []string
	for _, out := range []*[]string{&one, &two} {
		e := Split(bytes.NewReader(b), 1024, 4096, 16384, func(c []byte) error {
			if len(c) > 16384 {
				t.Fatal("oversized chunk")
			}
			*out = append(*out, Hash(c))
			return nil
		})
		if e != nil {
			t.Fatal(e)
		}
	}
	if strings.Join(one, ",") != strings.Join(two, ",") {
		t.Fatal("nondeterministic boundaries")
	}
	if len(one) < 10 {
		t.Fatal("not enough content-defined boundaries")
	}
}
func TestCDCInsertionResynchronizes(t *testing.T) {
	before := data(2 << 20)
	after := append(append(append([]byte{}, before[:700001]...), data(1234)...), before[700001:]...)
	old := chunkSet(t, before)
	next := chunkSet(t, after)
	reused := 0
	for k, n := range next {
		if _, ok := old[k]; ok {
			reused += n
		}
	}
	if reused < len(before)*90/100 {
		t.Fatalf("insufficient insertion reuse: %d / %d", reused, len(before))
	}
}
func TestCompressedAndRawIntegrity(t *testing.T) {
	raw := data(10240)
	enc, _ := Encode(raw)
	c := Chunk{Hash(raw), int64(len(raw)), Hash(enc), int64(len(enc))}
	if got, e := Decode(enc, c); e != nil || !bytes.Equal(got, raw) {
		t.Fatal(e)
	}
	enc[len(enc)/2] ^= 1
	if _, e := Decode(enc, c); e == nil {
		t.Fatal("corruption accepted")
	}
	c.EncodedSHA256 = Hash(enc)
	if _, e := Decode(enc, c); e == nil {
		t.Fatal("invalid compressed content accepted")
	}
}

type rig struct {
	dir, root, key, pub, state string
	s                          *FaultServer
	http                       *httptest.Server
}

func newRig(t *testing.T, plan FaultPlan) *rig {
	t.Helper()
	d := t.TempDir()
	keys := filepath.Join(d, "keys")
	if e := Keygen(keys); e != nil {
		t.Fatal(e)
	}
	r := &rig{dir: d, root: filepath.Join(d, "origin"), key: filepath.Join(keys, "publisher.key"), pub: filepath.Join(keys, "publisher.pub"), state: filepath.Join(d, "edge")}
	r.s = NewFaultServer(r.root, plan)
	r.http = httptest.NewServer(r.s)
	t.Cleanup(r.http.Close)
	return r
}
func (r *rig) publish(t *testing.T, name string, seq uint64, b []byte) Manifest {
	t.Helper()
	in := filepath.Join(r.dir, name+".tar")
	if e := os.WriteFile(in, b, 0600); e != nil {
		t.Fatal(e)
	}
	m, e := Publish(PublishOptions{Input: in, Root: r.root, Key: r.key, Release: name, Sequence: seq, Kind: "raw", Min: 1024, Avg: 4096, Max: 16384})
	if e != nil {
		t.Fatal(e)
	}
	return m
}
func (r *rig) options(name string) AgentOptions {
	o := DefaultAgentOptions()
	o.StateDir = r.state
	o.PublicKey = r.pub
	o.ManifestURL = r.http.URL + "/releases/" + name + ".json"
	o.BaseURL = r.http.URL
	o.AllowHTTP = true
	o.Backoff = time.Millisecond
	o.MaxBackoff = 5 * time.Millisecond
	o.RequestTimeout = time.Second
	o.ReserveBytes = 0
	o.Workers = 2
	o.Events = io.Discard
	return o
}
func mustSync(t *testing.T, r *rig, name string) Summary {
	t.Helper()
	s, e := Sync(context.Background(), r.options(name))
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func TestDeltaRepeatAndVersionSkip(t *testing.T) {
	r := newRig(t, FaultPlan{})
	b := data(512 << 10)
	m1 := r.publish(t, "v1", 1, b)
	first := mustSync(t, r, "v1")
	if first.DownloadedChunks != int64(len(Unique(m1))) {
		t.Fatal("cold cache count mismatch")
	}
	changed := append([]byte{}, b...)
	copy(changed[200000:201000], bytes.Repeat([]byte{0x17}, 1000))
	r.publish(t, "v3", 3, changed)
	delta := mustSync(t, r, "v3")
	if delta.ReusedChunks == 0 || delta.DownloadedRawBytes > int64(len(b))/4 {
		t.Fatalf("delta not effective: %+v", delta)
	}
	repeat := mustSync(t, r, "v3")
	if repeat.ChunkRequests != 0 || repeat.ChunkBodyBytes != 0 || repeat.MetadataBodyBytes != 0 {
		t.Fatal("repeat fetched payload")
	}
	if _, e := Sync(context.Background(), r.options("v1")); e == nil || !strings.Contains(e.Error(), "rollback") {
		t.Fatal("rollback not rejected", e)
	}
}
func TestTruncatedHTTPRetriesOnlyIncompleteChunks(t *testing.T) {
	r := newRig(t, FaultPlan{DropEvery: 5, DropAfterBytes: 123})
	m := r.publish(t, "v1", 1, data(256<<10))
	s := mustSync(t, r, "v1")
	stats := r.s.Snapshot()
	if stats.Drops == 0 || s.Retries == 0 || s.RetryOverheadBytes == 0 {
		t.Fatalf("faults not exercised: %+v %+v", s, stats)
	}
	if s.DownloadedChunks != int64(len(Unique(m))) {
		t.Fatal("committed count mismatch")
	}
	if s.RetryOverheadBytes != stats.Drops*123 {
		t.Fatalf("unexpected duplicate payload %d, drops=%d", s.RetryOverheadBytes, stats.Drops)
	}
}
func TestCorruptResponseIsRetried(t *testing.T) {
	r := newRig(t, FaultPlan{CorruptFirst: 1})
	r.publish(t, "v1", 1, data(128<<10))
	s := mustSync(t, r, "v1")
	if s.IntegrityFailures != 1 || s.RetryOverheadBytes == 0 {
		t.Fatalf("corruption not rejected %+v", s)
	}
}
func TestExistingCacheCorruptionIsRepaired(t *testing.T) {
	r := newRig(t, FaultPlan{})
	m := r.publish(t, "v1", 1, data(128<<10))
	mustSync(t, r, "v1")
	c := Unique(m)[0]
	if e := os.WriteFile(CachePath(r.state, c), bytes.Repeat([]byte{9}, int(c.Size)), 0600); e != nil {
		t.Fatal(e)
	}
	s := mustSync(t, r, "v1")
	if s.DownloadedChunks != 1 || !ValidChunk(r.state, c) {
		t.Fatalf("did not repair only the bad chunk: %+v", s)
	}
}
func TestBadSignatureFetchesNoChunks(t *testing.T) {
	r := newRig(t, FaultPlan{})
	r.publish(t, "v1", 1, data(128<<10))
	p := filepath.Join(r.root, "releases", "v1.json")
	b, _ := os.ReadFile(p)
	var env Envelope
	_ = json.Unmarshal(b, &env)
	decoded, _ := base64.StdEncoding.DecodeString(env.Payload)
	decoded[15] ^= 1
	env.Payload = base64.StdEncoding.EncodeToString(decoded)
	b, _ = json.Marshal(env)
	_ = os.WriteFile(p, b, 0600)
	if _, e := Sync(context.Background(), r.options("v1")); e == nil || !strings.Contains(e.Error(), "signature") {
		t.Fatal("tampered signature accepted", e)
	}
	if r.s.Snapshot().ChunkRequests != 0 {
		t.Fatal("requested chunks before authenticating release")
	}
}
func TestSameSequenceEquivocationRejected(t *testing.T) {
	r := newRig(t, FaultPlan{})
	r.publish(t, "v1", 1, data(128<<10))
	mustSync(t, r, "v1")
	r.publish(t, "other", 1, data(256<<10))
	if _, e := Sync(context.Background(), r.options("other")); e == nil || !strings.Contains(e.Error(), "equivocation") {
		t.Fatal("same-sequence replacement accepted", e)
	}
}
func TestOfflineReassemblyMakesNoRequests(t *testing.T) {
	r := newRig(t, FaultPlan{})
	r.publish(t, "v1", 1, data(128<<10))
	s := mustSync(t, r, "v1")
	if e := os.Remove(s.Artifact); e != nil {
		t.Fatal(e)
	}
	before := r.s.Snapshot()
	r.http.Close()
	o := r.options("v1")
	o.Offline = true
	s, e := Sync(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if s.ChunkRequests != 0 || s.MetadataRequests != 0 || r.s.Snapshot().Requests != before.Requests {
		t.Fatal("offline used HTTP")
	}
}
func TestOutageAndTimeoutRecover(t *testing.T) {
	t.Run("503", func(t *testing.T) {
		r := newRig(t, FaultPlan{FailFirst: 2})
		r.publish(t, "v1", 1, data(32768))
		s := mustSync(t, r, "v1")
		if s.Retries < 2 {
			t.Fatal("503 retry not exercised")
		}
	})
	t.Run("stall", func(t *testing.T) {
		r := newRig(t, FaultPlan{StallFirst: 1, StallMS: 100})
		r.publish(t, "v1", 1, data(65536))
		o := r.options("v1")
		o.RequestTimeout = 20 * time.Millisecond
		s, e := Sync(context.Background(), o)
		if e != nil {
			t.Fatal(e)
		}
		if s.Retries == 0 {
			t.Fatal("deadline retry not exercised")
		}
	})
}

type eventWriterFunc func([]byte) (int, error)

func (f eventWriterFunc) Write(b []byte) (int, error) { return f(b) }

func TestCancellationPreservesCompletedChunks(t *testing.T) {
	r := newRig(t, FaultPlan{RateKbit: 1000})
	m := r.publish(t, "v1", 1, data(256<<10))
	o := r.options("v1")
	o.Workers = 1
	// Metadata and fsync latency are platform-dependent. Cancel synchronously
	// after AtomicWrite has completed, not after an assumed transfer duration.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var committed string
	o.Events = eventWriterFunc(func(b []byte) (int, error) {
		var event struct {
			Event string `json:"event"`
			Chunk string `json:"chunk"`
		}
		if e := json.Unmarshal(b, &event); e != nil {
			return 0, e
		}
		if event.Event == "chunk_committed" && committed == "" {
			committed = event.Chunk
			cancel()
		}
		return len(b), nil
	})
	summary, e := Sync(ctx, o)
	if !errors.Is(e, context.Canceled) || committed == "" {
		t.Fatalf("wanted cancellation after durable progress; chunk=%q summary=%+v error=%v", committed, summary, e)
	}
	cached := map[string]bool{}
	for _, c := range Unique(m) {
		if ValidChunk(r.state, c) {
			cached[BlobRel(c)] = true
		}
	}
	if len(cached) == 0 || len(cached) == len(Unique(m)) {
		t.Fatalf("did not interrupt mid-transfer: valid=%d total=%d", len(cached), len(Unique(m)))
	}
	if summary.DownloadedChunks != int64(len(cached)) {
		t.Fatalf("reported commits=%d, validated cache=%d", summary.DownloadedChunks, len(cached))
	}
	t.Logf("canceled after durable chunk %s: validated %d/%d chunks", committed, len(cached), len(Unique(m)))
	before := r.s.Snapshot()
	mustSync(t, r, "v1")
	after := r.s.Snapshot()
	for path := range cached {
		if after.ObjectRequests[path] != before.ObjectRequests[path] {
			t.Fatalf("durable chunk re-requested %s", path)
		}
	}
}
func TestAggregateThrottlingAcrossWorkers(t *testing.T) {
	r := newRig(t, FaultPlan{RateKbit: 2000})
	r.publish(t, "v1", 1, data(128<<10))
	o := r.options("v1")
	o.Workers = 4
	s, e := Sync(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	minimum := float64(s.ChunkBodyBytes+s.MetadataBodyBytes) * 8 / 2e6
	if s.Seconds < minimum*0.9 {
		t.Fatalf("rate cap bypassed by concurrency: %.3f < %.3f", s.Seconds, minimum)
	}
}
func TestDiskPreflightAndSingleWriterLock(t *testing.T) {
	r := newRig(t, FaultPlan{})
	r.publish(t, "v1", 1, data(32768))
	o := r.options("v1")
	o.ReserveBytes = 1 << 60
	if _, e := Sync(context.Background(), o); e == nil || !strings.Contains(e.Error(), "insufficient space") {
		t.Fatal("disk guard failed", e)
	}
	if r.s.Snapshot().ChunkRequests != 0 {
		t.Fatal("downloaded before checking space")
	}
	lock, e := LockState(r.state)
	if e != nil {
		t.Fatal(e)
	}
	defer UnlockState(lock)
	if _, e = Sync(context.Background(), r.options("v1")); e == nil || !strings.Contains(e.Error(), "another agent") {
		t.Fatal("lock failed", e)
	}
}
func TestBoundsAndKeyValidation(t *testing.T) {
	r := newRig(t, FaultPlan{})
	m := r.publish(t, "v1", 1, data(32768))
	key, _ := ReadKey(r.key, ed25519.PrivateKeySize)
	pub, _ := ReadKey(r.pub, ed25519.PublicKeySize)
	m.Chunks[0].Size = 1 << 40
	b, _ := Sign(m, key)
	if _, _, e := Verify(b, pub, 1<<30); e == nil {
		t.Fatal("unbounded chunk accepted")
	}
	if e := allowedURL("http://example.com/x", false); e == nil {
		t.Fatal("insecure origin accepted without opt-in")
	}
}
func TestPublisherDeterminismAndImmutableNames(t *testing.T) {
	r := newRig(t, FaultPlan{})
	b := data(32768)
	one := r.publish(t, "v1", 1, b)
	two := r.publish(t, "v1", 1, b)
	a, _ := json.Marshal(one)
	c, _ := json.Marshal(two)
	if !bytes.Equal(a, c) {
		t.Fatal("publish was not deterministic in the same build")
	}
	in := filepath.Join(r.dir, "different")
	_ = os.WriteFile(in, data(65536), 0600)
	_, e := Publish(PublishOptions{Input: in, Root: r.root, Key: r.key, Release: "v1", Sequence: 1, Kind: "raw", Min: 1024, Avg: 4096, Max: 16384})
	if e == nil {
		t.Fatal("overwrote immutable release")
	}
}
