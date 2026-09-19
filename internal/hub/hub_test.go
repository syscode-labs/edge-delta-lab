package hub

import (
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"example.com/edge-delta-lab/internal/lab"
)

// countingLoader counts underlying reads and sleeps so concurrent requests
// overlap while one read is in flight.
func countingLoader(reads *atomic.Int64, delay time.Duration) Loader {
	return func(rel string) ([]byte, error) {
		reads.Add(1)
		if delay > 0 {
			time.Sleep(delay)
		}
		return []byte("body-of-" + rel), nil
	}
}

// waitFor polls cond until it holds; the hub observes served requests from the
// server handler goroutine, which can complete shortly after the client's body
// read returns, so black-box metric assertions must be eventually-consistent.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

func TestSingleflightCoalescesConcurrentReads(t *testing.T) {
	var reads atomic.Int64
	h := New(countingLoader(&reads, 100*time.Millisecond), 1<<20)
	const n = 50
	var wg sync.WaitGroup
	var wgSync sync.Mutex
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b, err := h.Loader()("chunks/ab/deadbeef.gz")
			wgSync.Lock()
			errs[i] = err
			wgSync.Unlock()
			if err == nil && string(b) != "body-of-chunks/ab/deadbeef.gz" {
				t.Errorf("bad body %q", b)
			}
		}(i)
	}
	wg.Wait()
	if got := reads.Load(); got != 1 {
		t.Fatalf("want exactly 1 underlying read, got %d", got)
	}
	st := h.Snapshot()
	if st.Coalesced != n-1 {
		t.Fatalf("coalesced=%d want %d", st.Coalesced, n-1)
	}
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestLRURespectsByteBudgetAndEvicts(t *testing.T) {
	var reads atomic.Int64
	h := New(countingLoader(&reads, 0), 100) // 100-byte budget
	load := h.Loader()
	for i := 0; i < 10; i++ {
		if _, err := load(fmt.Sprintf("releases/r%d.json", i)); err != nil { // ~20 bytes each
			t.Fatal(err)
		}
	}
	st := h.Snapshot()
	if st.CacheBytes > 100 {
		t.Fatalf("cache %d > budget 100", st.CacheBytes)
	}
	if st.Evictions == 0 {
		t.Fatal("expected evictions")
	}
	// Oldest objects must have been evicted -> re-read hits disk.
	before := reads.Load()
	if _, err := load("releases/r0.json"); err != nil {
		t.Fatal(err)
	}
	if reads.Load() != before+1 {
		t.Fatal("r0 should have been evicted")
	}
}

func TestFaultServerHubWiringPreservesV2(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "releases"), 0o755); err != nil {
		t.Fatal(err)
	}
	env := []byte(`{"release":"r1"}`)
	if err := os.WriteFile(filepath.Join(dir, "releases", "desired.json"), env, 0o644); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	// The hub's UNDERLYING loader must read the real file so the served body
	// matches the on-disk env; counting wraps os.ReadFile.
	underlying := func(rel string) ([]byte, error) {
		reads.Add(1)
		return os.ReadFile(filepath.Join(dir, rel))
	}
	h := New(underlying, 1<<20)
	srv := lab.NewFaultServer(dir, lab.FaultPlan{})
	srv.ObjectLoader = h.Loader()
	srv.DeviceHeader = "X-Edgelab-Device"
	srv.ServeObserver = h.Record
	ts := httptest.NewServer(srv)
	defer ts.Close()

	// 200 + ETag (device header present so hub metrics attribute both fetches).
	req1, _ := http.NewRequest("GET", ts.URL+"/releases/desired.json", nil)
	req1.Header.Set("X-Edgelab-Device", "edge-01")
	resp, err := http.DefaultClient.Do(req1)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(b) != string(env) {
		t.Fatalf("status=%d body=%q", resp.StatusCode, b)
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		t.Fatal("missing ETag")
	}
	// 304.
	req, _ := http.NewRequest("GET", ts.URL+"/releases/desired.json", nil)
	req.Header.Set("If-None-Match", etag)
	req.Header.Set("X-Edgelab-Device", "edge-01")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotModified {
		t.Fatalf("want 304, got %d", resp.StatusCode)
	}
	// Hub counted the device: the 200 served len(env) bytes, the 304 added 0.
	waitFor(t, 2*time.Second, func() bool {
		st := h.Snapshot()
		return len(st.Devices) == 1 && st.Devices[0].Requests == 2
	}, "device edge-01 counted for both requests")
	st := h.Snapshot()
	if len(st.Devices) != 1 || st.Devices[0].Device != "edge-01" ||
		st.Devices[0].Requests != 2 || st.Devices[0].BytesServed != int64(len(env)) {
		t.Fatalf("devices: %+v", st.Devices)
	}
	// Cached second fetch: exactly one disk read so far.
	if reads.Load() != 1 {
		t.Fatalf("want 1 disk read, got %d", reads.Load())
	}
}

func TestFiftyConcurrentClientsOneUnderlyingReadPerColdObject(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "chunks", "ab"), 0o755); err != nil {
		t.Fatal(err)
	}
	chunk := make([]byte, 4096)
	rand.Read(chunk)
	// v2's isChunk route requires a 64-char hex chunk name.
	chunkName := strings.Repeat("a", 64) + ".gz"
	if err := os.WriteFile(filepath.Join(dir, "chunks", "ab", chunkName), chunk, 0o644); err != nil {
		t.Fatal(err)
	}
	var reads atomic.Int64
	// Underlying loader reads the real on-disk chunk; 30ms delay overlaps the
	// 50 concurrent requests while one read is in flight.
	slow := func(rel string) ([]byte, error) {
		reads.Add(1)
		time.Sleep(30 * time.Millisecond)
		return os.ReadFile(filepath.Join(dir, rel))
	}
	h := New(slow, 1<<20)
	srv := lab.NewFaultServer(dir, lab.FaultPlan{})
	srv.ObjectLoader = h.Loader()
	srv.DeviceHeader = "X-Edgelab-Device"
	srv.ServeObserver = h.Record
	ts := httptest.NewServer(srv)
	defer ts.Close()

	const n = 50
	var wg sync.WaitGroup
	errs := make([]error, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req, _ := http.NewRequest("GET", ts.URL+"/chunks/ab/"+chunkName, nil)
			req.Header.Set("X-Edgelab-Device", fmt.Sprintf("spoke-%02d", i))
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				errs[i] = err
				return
			}
			defer resp.Body.Close()
			b, err := io.ReadAll(resp.Body)
			if err != nil {
				errs[i] = err
				return
			}
			if resp.StatusCode != 200 || len(b) != len(chunk) {
				errs[i] = fmt.Errorf("spoke %d: status=%d len=%d", i, resp.StatusCode, len(b))
			}
		}(i)
	}
	wg.Wait()
	elapsed := time.Since(start)
	// Metrics land on handler completion, which trails the client's body read.
	waitFor(t, 5*time.Second, func() bool {
		st := h.Snapshot()
		return len(st.Devices) == n && st.Coalesced == n-1
	}, "all 50 device records + coalesced count")
	for i, err := range errs {
		if err != nil {
			t.Fatalf("spoke %d: %v", i, err)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("want exactly 1 underlying read for the cold object, got %d", got)
	}
	st := h.Snapshot()
	if len(st.Devices) != n {
		t.Fatalf("want %d devices, got %d", n, len(st.Devices))
	}
	var total int64
	for _, d := range st.Devices {
		if d.BytesServed != int64(len(chunk)) {
			t.Fatalf("%s served %d != %d", d.Device, d.BytesServed, len(chunk))
		}
		total += d.BytesServed
	}
	if st.Coalesced != n-1 {
		t.Fatalf("coalesced=%d want %d", st.Coalesced, n-1)
	}
	t.Logf("N=%d concurrent clients served in %s (1 disk read, %d coalesced, %d total bytes)", n, elapsed, st.Coalesced, total)
}
