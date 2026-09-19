// Package exporter exposes bounded scalar telemetry from an edgelab admin socket.
package exporter

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Socket, Role, Instance string
	Timeout                time.Duration
	MaxResponse            int64
}
type Exporter struct {
	cfg         Config
	start       time.Time
	mu          sync.Mutex
	lastSuccess float64
}

func New(c Config) *Exporter {
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Second
	}
	if c.MaxResponse <= 0 {
		c.MaxResponse = 1 << 20
	}
	return &Exporter{cfg: c, start: time.Now()}
}
func (e *Exporter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	_, _ = io.WriteString(w, e.scrape(r.Context()))
}
func (e *Exporter) scrape(ctx context.Context) string {
	labels := fmt.Sprintf(`job="edgelab",instance="%s",role="%s"`, esc(e.cfg.Instance), esc(e.cfg.Role))
	var out strings.Builder
	b, err := e.fetch(ctx)
	if err != nil {
		fmt.Fprintf(&out, "edgelab_source_up{%s} 0\n", labels)
		e.mu.Lock()
		last := e.lastSuccess
		e.mu.Unlock()
		if last > 0 {
			fmt.Fprintf(&out, "edgelab_snapshot_last_success_timestamp_seconds{%s} %.3f\n", labels, last)
		}
		return out.String()
	}
	now := float64(time.Now().UnixNano()) / 1e9
	e.mu.Lock()
	e.lastSuccess = now
	e.mu.Unlock()
	fmt.Fprintf(&out, "edgelab_source_up{%s} 1\nedgelab_snapshot_last_success_timestamp_seconds{%s} %.3f\n", labels, labels, now)
	var root map[string]any
	if json.Unmarshal(b, &root) != nil {
		return out.String()
	}
	if e.cfg.Role == "hub" {
		emitHub(&out, labels, root)
	} else {
		emitClient(&out, labels, root)
	}
	start := num(root, "process_start_time_seconds")
	if x, ok := root["client"].(map[string]any); ok {
		start = num(x, "process_start_time_seconds")
	}
	if start == 0 {
		start = float64(e.start.UnixNano()) / 1e9
	}
	// Keep process start after role selection so client exporters report the
	// long-lived client process, not the scrape sidecar's start time.
	lines := out.String()
	marker := fmt.Sprintf("edgelab_source_up{%s} 1\n", labels)
	lines = strings.Replace(lines, marker, marker+fmt.Sprintf("edgelab_process_start_time_seconds{%s} %.3f\n", labels, start), 1)
	return lines
}
func emitHub(o *strings.Builder, l string, r map[string]any) {
	// Admin stats contains sibling origin (HTTP counters) and hub (cache)
	// snapshots. Keep each source separate; retain flat-snapshot support.
	origin := r
	if x, ok := r["origin"].(map[string]any); ok {
		origin = x
	}
	counterPair(o, "edgelab_hub_requests_total", l, num(origin, "chunk_requests"), num(origin, "metadata_requests"), "chunk", "metadata")
	counterPair(o, "edgelab_hub_response_body_bytes_total", l, num(origin, "chunk_response_body_bytes"), num(origin, "metadata_response_body_bytes"), "chunk", "metadata")
	if x, ok := r["hub"].(map[string]any); ok {
		r = x
	}
	gauge(o, "edgelab_hub_cache_bytes", l, num(r, "cache_bytes"))
	gauge(o, "edgelab_hub_cache_capacity_bytes", l, num(r, "cache_max_bytes"))
	gauge(o, "edgelab_hub_cache_objects", l, num(r, "cache_objects"))
	gauge(o, "edgelab_hub_inflight_reads", l, num(r, "inflight_reads"))
	counter(o, "edgelab_hub_disk_reads_total", l, num(r, "disk_reads"))
	counter(o, "edgelab_hub_cache_hits_total", l, num(r, "cache_hits"))
	counter(o, "edgelab_hub_coalesced_requests_total", l, num(r, "coalesced_requests"))
	counter(o, "edgelab_hub_cache_evictions_total", l, num(r, "evictions"))
}
func emitClient(o *strings.Builder, l string, r map[string]any) {
	if x, ok := r["client"].(map[string]any); ok {
		r = x
	}
	counterPair(o, "edgelab_client_response_body_bytes_total", l, num(r, "chunk_response_body_bytes"), num(r, "metadata_response_body_bytes"), "chunk", "metadata")
	counterPair(o, "edgelab_client_requests_total", l, num(r, "chunk_requests"), num(r, "metadata_requests"), "chunk", "metadata")
	counterPair(o, "edgelab_client_syncs_total", l, num(r, "sync_success"), num(r, "sync_failure"), "success", "failure")
	counter(o, "edgelab_client_retries_total", l, num(r, "retries"))
	counter(o, "edgelab_client_integrity_failures_total", l, num(r, "integrity_failures"))
	counter(o, "edgelab_client_verified_download_bytes_total", l, num(r, "verified_download_bytes"))
	counter(o, "edgelab_client_verified_chunks_total", l, num(r, "verified_chunks"))
	counter(o, "edgelab_client_reused_chunks_total", l, num(r, "reused_chunks"))
	gauge(o, "edgelab_client_sync_active", l, num(r, "sync_active"))
	state := fmt.Sprint(r["state"])
	if state == "" {
		state = "unknown"
	}
	fmt.Fprintf(o, "edgelab_client_state{%s,state=\"%s\"} 1\n", l, esc(state))
	gauge(o, "edgelab_client_last_sync_duration_seconds", l, num(r, "last_sync_duration_seconds"))
	gauge(o, "edgelab_client_last_success_timestamp_seconds", l, num(r, "last_success_timestamp_seconds"))
	gauge(o, "edgelab_client_last_sync_download_bytes", l, num(r, "last_sync_download_bytes"))
	gauge(o, "edgelab_client_last_sync_verified_chunks", l, num(r, "last_sync_verified_chunks"))
	gauge(o, "edgelab_client_last_sync_reused_chunks", l, num(r, "last_sync_reused_chunks"))
}
func num(m map[string]any, k string) float64 {
	v, ok := m[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case float64:
		return x
	case int64:
		return float64(x)
	case json.Number:
		f, _ := x.Float64()
		return f
	}
	return 0
}
func gauge(o *strings.Builder, n, l string, v float64) {
	fmt.Fprintf(o, "%s{%s} %s\n", n, l, strconv.FormatFloat(v, 'f', 6, 64))
}
func counter(o *strings.Builder, n, l string, v float64) {
	fmt.Fprintf(o, "%s{%s} %s\n", n, l, strconv.FormatFloat(v, 'f', 6, 64))
}
func counterPair(o *strings.Builder, n, l string, a, b float64, ka, kb string) {
	fmt.Fprintf(o, "%s{%s,kind=\"%s\"} %s\n%s{%s,kind=\"%s\"} %s\n", n, l, ka, strconv.FormatFloat(a, 'f', 6, 64), n, l, kb, strconv.FormatFloat(b, 'f', 6, 64))
}
func (e *Exporter) fetch(ctx context.Context) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, e.cfg.Timeout)
	defer cancel()
	c, err := (&net.Dialer{}).DialContext(cctx, "unix", e.cfg.Socket)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(e.cfg.Timeout))
	if _, err = fmt.Fprintln(c, `{"id":1,"method":"stats"}`); err != nil {
		return nil, err
	}
	b, err := bufio.NewReader(io.LimitReader(c, e.cfg.MaxResponse)).ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	var resp struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(b, &resp) != nil || !resp.OK {
		return nil, fmt.Errorf("admin stats failed")
	}
	if e.cfg.Role == "hub" {
		var root map[string]any
		if json.Unmarshal(resp.Result, &root) != nil || root == nil {
			return nil, fmt.Errorf("invalid hub stats snapshot")
		}
		cache := root
		if nested, exists := root["hub"]; exists {
			cache, _ = nested.(map[string]any)
		} else if _, wrapped := root["origin"]; wrapped {
			return nil, fmt.Errorf("missing hub cache snapshot")
		}
		// Zero is valid; absent or nonnumeric occupancy is not an empty cache.
		for _, key := range []string{"cache_bytes", "cache_objects", "cache_max_bytes"} {
			if _, ok := cache[key].(float64); !ok {
				return nil, fmt.Errorf("invalid hub cache field %s", key)
			}
		}
	}
	return resp.Result, nil
}
func esc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `
`, `\n`).Replace(s)
}
