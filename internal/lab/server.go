package lab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FaultPlan struct {
	RateKbit       int64 `json:"rate_kbit"`
	LatencyMS      int64 `json:"latency_ms"`
	Offline        bool  `json:"offline"`
	OfflineForMS   int64 `json:"offline_for_ms"`
	FailFirst      int64 `json:"fail_first"`
	DropEvery      int64 `json:"drop_every"`
	DropAfterBytes int64 `json:"drop_after_bytes"`
	CorruptFirst   int64 `json:"corrupt_first"`
	StallFirst     int64 `json:"stall_first"`
	StallMS        int64 `json:"stall_ms"`
}
type ServerStats struct {
	OriginStarted     string `json:"origin_started"`
	Requests          int64  `json:"requests"`
	ChunkRequests     int64  `json:"chunk_requests"`
	MetadataRequests  int64  `json:"metadata_requests"`
	ChunkBodyBytes    int64  `json:"chunk_response_body_bytes"`
	MetadataBodyBytes int64  `json:"metadata_response_body_bytes"`
	// Accepted body bytes on the second and later GET for each chunk path in
	// this origin epoch. This is not receiver-verified waste: distinct devices
	// and intentional re-fetches also repeat objects, and first-attempt corrupt
	// or truncated bodies are not included. Existing total counters include it.
	ChunkRetryBodyBytes int64            `json:"chunk_retry_response_body_bytes"`
	Drops               int64            `json:"injected_disconnects"`
	Failures            int64            `json:"injected_503s"`
	Corruptions         int64            `json:"injected_corruptions"`
	Stalls              int64            `json:"injected_stalls"`
	ObjectRequests      map[string]int64 `json:"object_requests"`
}

// Pacer is one aggregate response-body budget for this server/site, not per worker.
// It does not model packet loss or TCP retransmission; see TESTING.md.
type Pacer struct {
	mu   sync.Mutex
	next time.Time
}

func (p *Pacer) Wait(ctx context.Context, n int, rateKbit int64) error {
	if rateKbit <= 0 {
		return nil
	}
	p.mu.Lock()
	now := time.Now()
	if p.next.Before(now) {
		p.next = now
	}
	p.next = p.next.Add(time.Duration(float64(n) * 8 * float64(time.Second) / float64(rateKbit*1000)))
	d := time.Until(p.next)
	p.mu.Unlock()
	return sleepContext(ctx, d)
}

type FaultServer struct {
	Root, PlanFile string
	ReceiptsDir    string
	Plan           FaultPlan
	Started        time.Time
	// ObjectLoader, when set, replaces the direct os.ReadFile for object
	// bodies (chunks + metadata). The v3 hub uses it for singleflight + LRU;
	// nil (default) keeps exact v2 behavior.
	ObjectLoader func(rel string) ([]byte, error)
	// DeviceHeader names the request header identifying the client (e.g.
	// X-Edgelab-Device); empty disables per-device observation.
	DeviceHeader string
	// ServeObserver, when set with DeviceHeader, receives per-request served
	// byte counts for the hub's per-client metrics.
	ServeObserver func(device string, isChunk bool, bytes int64)
	// Events, when set, is served at /events (websocket announce channel);
	// nil (default) leaves /events unmatched so behavior is unchanged.
	Events http.Handler
	mu           sync.Mutex
	stats        ServerStats
	pacer        Pacer
}

func NewFaultServer(root string, plan FaultPlan) *FaultServer {
	return &FaultServer{Root: root, Plan: plan, Started: time.Now(), stats: ServerStats{ObjectRequests: map[string]int64{}}}
}
func (s *FaultServer) Snapshot() ServerStats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.OriginStarted = s.Started.UTC().Format(time.RFC3339Nano)
	out.ObjectRequests = map[string]int64{}
	for k, v := range s.stats.ObjectRequests {
		out.ObjectRequests[k] = v
	}
	return out
}
func (s *FaultServer) plan() (FaultPlan, error) {
	if s.PlanFile == "" {
		return s.Plan, nil
	}
	b, e := os.ReadFile(s.PlanFile)
	if e != nil {
		return FaultPlan{}, e
	}
	var p FaultPlan
	e = json.Unmarshal(b, &p)
	return p, e
}
func (s *FaultServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/receipts":
		s.receipts(w, r)
		return
	case "/healthz":
		w.WriteHeader(200)
		_, _ = w.Write([]byte("ok\n"))
		return
	case "/stats":
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Snapshot())
		return
	case "/metrics":
		st := s.Snapshot()
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = fmt.Fprintf(w, "# TYPE edge_origin_chunk_body_bytes_total counter\nedge_origin_chunk_body_bytes_total %d\n# TYPE edge_origin_chunk_requests_total counter\nedge_origin_chunk_requests_total %d\n# TYPE edge_origin_injected_disconnects_total counter\nedge_origin_injected_disconnects_total %d\n# TYPE edge_origin_injected_503s_total counter\nedge_origin_injected_503s_total %d\n", st.ChunkBodyBytes, st.ChunkRequests, st.Drops, st.Failures)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	rel := strings.TrimPrefix(r.URL.Path, "/")
	if rel == "events" && s.Events != nil {
		s.Events.ServeHTTP(w, r)
		return
	}
	parts := strings.Split(rel, "/")
	isChunk := len(parts) == 3 && parts[0] == "chunks" && len(parts[1]) == 2 && strings.HasSuffix(parts[2], ".gz") && hexDigest.MatchString(strings.TrimSuffix(parts[2], ".gz"))
	isMeta := len(parts) == 2 && parts[0] == "releases" && strings.HasSuffix(parts[1], ".json") && safeRelease.MatchString(strings.TrimSuffix(parts[1], ".json"))
	if !isChunk && !isMeta {
		http.NotFound(w, r)
		return
	}
	plan, e := s.plan()
	if e != nil {
		http.Error(w, "invalid fault plan", 500)
		return
	}
	s.mu.Lock()
	s.stats.Requests++
	requestNo := s.stats.Requests
	var chunkNo int64
	var repeatedChunk bool
	if isChunk {
		s.stats.ChunkRequests++
		chunkNo = s.stats.ChunkRequests
		s.stats.ObjectRequests[rel]++
		repeatedChunk = s.stats.ObjectRequests[rel] > 1
	} else {
		s.stats.MetadataRequests++
	}
	s.mu.Unlock()
	if e = sleepContext(r.Context(), time.Duration(plan.LatencyMS)*time.Millisecond); e != nil {
		return
	}
	offline := plan.Offline || (plan.OfflineForMS > 0 && time.Since(s.Started) < time.Duration(plan.OfflineForMS)*time.Millisecond)
	if offline || requestNo <= plan.FailFirst {
		s.mu.Lock()
		s.stats.Failures++
		s.mu.Unlock()
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(503)
		return
	}
	if isChunk && plan.StallFirst > 0 && chunkNo <= plan.StallFirst {
		s.mu.Lock()
		s.stats.Stalls++
		s.mu.Unlock()
		if e = sleepContext(r.Context(), time.Duration(plan.StallMS)*time.Millisecond); e != nil {
			return
		}
	}
	path := filepath.Join(s.Root, filepath.FromSlash(rel))
	var data []byte
	if s.ObjectLoader != nil {
		data, e = s.ObjectLoader(rel)
	} else {
		data, e = os.ReadFile(path)
	}
	if e != nil {
		http.NotFound(w, r)
		return
	}
	if isMeta {
		etag := "\"" + Hash(data) + "\""
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			s.observe(r, isChunk, 0)
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	corrupt := isChunk && chunkNo <= plan.CorruptFirst
	if corrupt && len(data) > 0 {
		data[len(data)/2] ^= 0x40
		s.mu.Lock()
		s.stats.Corruptions++
		s.mu.Unlock()
	}
	drop := isChunk && plan.DropEvery > 0 && chunkNo%plan.DropEvery == 0
	limit := len(data)
	if drop {
		n := int(plan.DropAfterBytes)
		if n <= 0 {
			n = 4096
		}
		if n >= limit {
			n = limit / 2
		}
		limit = n
		s.mu.Lock()
		s.stats.Drops++
		s.mu.Unlock()
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprint(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	if drop {
		w.Header().Set("Connection", "close")
	}
	w.WriteHeader(200)
	sent := 0
	for sent < limit {
		end := sent + 8192
		if end > limit {
			end = limit
		}
		if e = s.pacer.Wait(r.Context(), end-sent, plan.RateKbit); e != nil {
			return
		}
		n, err := w.Write(data[sent:end])
		sent += n
		s.mu.Lock()
		if isChunk {
			s.stats.ChunkBodyBytes += int64(n)
			if repeatedChunk {
				s.stats.ChunkRetryBodyBytes += int64(n)
			}
		} else {
			s.stats.MetadataBodyBytes += int64(n)
		}
		s.mu.Unlock()
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		if err != nil {
			return
		}
	}
	s.observe(r, isChunk, int64(sent))
	if drop {
		// Real truncated HTTP response: Content-Length advertises the full object,
		// then the socket is closed. Clients must reject the incomplete body.
		if h, ok := w.(http.Hijacker); ok {
			conn, b, e := h.Hijack()
			if e == nil {
				_ = b.Flush()
				_ = conn.Close()
			}
		}
	}
}

// observe reports one served request to the hub observer when configured.
func (s *FaultServer) observe(r *http.Request, isChunk bool, bytes int64) {
	if s.ServeObserver == nil || s.DeviceHeader == "" {
		return
	}
	s.ServeObserver(r.Header.Get(s.DeviceHeader), isChunk, bytes)
}

// Serve shuts down when ctx ends; tests use httptest with the same handler.
func Serve(ctx context.Context, addr string, s *FaultServer) error {
	srv := &http.Server{Addr: addr, Handler: s, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(c)
		case <-done:
		}
	}()
	e := srv.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}

var _ http.Handler = (*FaultServer)(nil)
