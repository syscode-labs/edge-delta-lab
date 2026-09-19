package exporter

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/edge-delta-lab/internal/hub"
	"example.com/edge-delta-lab/internal/lab"
)

// Match cmd/edgelab's admin Stats callback: origin and hub are siblings,
// serialized using their actual producer types, not a flattened snapshot.
func hubStatsResponse(t *testing.T) string {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"id": 1, "ok": true,
		"result": map[string]any{
			"origin": lab.ServerStats{
				Requests: 13, ChunkRequests: 11, MetadataRequests: 2,
				ChunkBodyBytes: 1234, MetadataBodyBytes: 56,
				ObjectRequests: map[string]int64{"objects/private-object": 11},
			},
			"hub": hub.Stats{
				CacheBytes: 7, CacheMaxBytes: 100, CacheObjects: 3,
				InflightReads: 4, DiskReads: 5, CacheHits: 6, Coalesced: 8, Evictions: 9,
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(b) + "\n"
}

func serveAdminResponses(t *testing.T, responses ...string) string {
	t.Helper()
	// Keep Unix socket paths short even when the platform's test temp root is long.
	dir, err := os.MkdirTemp("/tmp", "edgelab-exporter-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "admin.sock")
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for _, response := range responses {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.SetDeadline(time.Now().Add(time.Second))
			request, err := bufio.NewReader(c).ReadString('\n')
			if err == nil && request != "{\"id\":1,\"method\":\"stats\"}\n" {
				t.Errorf("unexpected admin request: %q", request)
			}
			if err == nil {
				_, _ = c.Write([]byte(response))
			}
			_ = c.Close()
		}
	}()
	return p
}

func scrapeMetrics(ex *Exporter) string {
	r := httptest.NewRecorder()
	ex.ServeHTTP(r, httptest.NewRequest("GET", "/metrics", nil))
	return r.Body.String()
}

func TestExporterScrapesBoundedAdminStats(t *testing.T) {
	p := serveAdminResponses(t, hubStatsResponse(t))
	ex := New(Config{Socket: p, Role: "hub", Instance: "test", Timeout: time.Second})
	s := scrapeMetrics(ex)
	labels := `job="edgelab",instance="test",role="hub"`
	if !strings.Contains(s, "edgelab_source_up{"+labels+"} 1\n") {
		t.Fatalf("source not up: %s", s)
	}
	for metric, value := range map[string]int{
		"edgelab_hub_cache_bytes":              7,
		"edgelab_hub_cache_capacity_bytes":     100,
		"edgelab_hub_cache_objects":            3,
		"edgelab_hub_inflight_reads":           4,
		"edgelab_hub_disk_reads_total":         5,
		"edgelab_hub_cache_hits_total":         6,
		"edgelab_hub_coalesced_requests_total": 8,
		"edgelab_hub_cache_evictions_total":    9,
	} {
		want := fmt.Sprintf("%s{%s} %d.000000\n", metric, labels, value)
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in metrics:\n%s", want, s)
		}
	}
	for _, sample := range []struct {
		metric, kind string
		value        int
	}{
		{"edgelab_hub_requests_total", "chunk", 11},
		{"edgelab_hub_requests_total", "metadata", 2},
		{"edgelab_hub_response_body_bytes_total", "chunk", 1234},
		{"edgelab_hub_response_body_bytes_total", "metadata", 56},
	} {
		want := fmt.Sprintf("%s{%s,kind=%q} %d.000000\n", sample.metric, labels, sample.kind, sample.value)
		if !strings.Contains(s, want) {
			t.Errorf("missing origin counter %q in metrics:\n%s", want, s)
		}
	}
	for _, forbidden := range []string{"go_gc_", "process_resident", "private-object", "object_requests"} {
		if strings.Contains(s, forbidden) {
			t.Errorf("unexpected unbounded/runtime metric %q: %s", forbidden, s)
		}
	}
}

func TestExporterEmptyHubCache(t *testing.T) {
	for _, snapshot := range []string{
		`{"origin":{},"hub":{"cache_bytes":0,"cache_objects":0,"cache_max_bytes":0}}`,
		`{"cache_bytes":0,"cache_objects":0,"cache_max_bytes":0}`,
	} {
		p := serveAdminResponses(t, `{"ok":true,"result":`+snapshot+"}\n")
		s := scrapeMetrics(New(Config{Socket: p, Role: "hub", Instance: "test"}))
		if !strings.Contains(s, "edgelab_source_up{job=\"edgelab\",instance=\"test\",role=\"hub\"} 1\n") ||
			!strings.Contains(s, "edgelab_hub_cache_bytes{job=\"edgelab\",instance=\"test\",role=\"hub\"} 0.000000\n") {
			t.Fatalf("valid empty cache rejected: %s", s)
		}
	}
}

func TestExporterSourceFailure(t *testing.T) {
	for _, role := range []string{"hub", "client"} {
		t.Run(role, func(t *testing.T) {
			ex := New(Config{Socket: "/does/not/exist", Role: role, Instance: "test", Timeout: 10 * time.Millisecond})
			want := fmt.Sprintf("edgelab_source_up{job=\"edgelab\",instance=\"test\",role=%q} 0\n", role)
			if got := scrapeMetrics(ex); got != want {
				t.Fatalf("failed source emitted unexpected metrics: %s", got)
			}
		})
	}
}

func TestExporterHubSourceFailureAfterSuccess(t *testing.T) {
	for _, tc := range []struct {
		name, response string
	}{
		{"rejected", "{\"ok\":false,\"error\":\"unavailable\"}\n"},
		{"missing-result", "{\"ok\":true}\n"},
		{"null-result", "{\"ok\":true,\"result\":null}\n"},
		{"missing-hub", "{\"ok\":true,\"result\":{\"origin\":{}}}\n"},
		{"null-hub", "{\"ok\":true,\"result\":{\"origin\":{},\"hub\":null}}\n"},
		{"wrong-hub-type", "{\"ok\":true,\"result\":{\"hub\":[]}}\n"},
		{"missing-occupancy", "{\"ok\":true,\"result\":{\"hub\":{}}}\n"},
		{"nonnumeric-occupancy", "{\"ok\":true,\"result\":{\"hub\":{\"cache_bytes\":\"7\",\"cache_objects\":3,\"cache_max_bytes\":100}}}\n"},
		{"malformed", "not JSON\n"},
		{"truncated", "{\"ok\":true"},
		{"oversized", strings.Repeat(" ", 4096) + "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := serveAdminResponses(t, hubStatsResponse(t), tc.response)
			ex := New(Config{Socket: p, Role: "hub", Instance: "test", Timeout: time.Second, MaxResponse: 4096})
			first := scrapeMetrics(ex)
			var lastSuccess string
			for _, line := range strings.Split(first, "\n") {
				if strings.HasPrefix(line, "edgelab_snapshot_last_success_timestamp_seconds{") {
					lastSuccess = line + "\n"
				}
			}
			if lastSuccess == "" || ex.lastSuccess <= 0 {
				t.Fatalf("initial scrape did not succeed: %s", first)
			}
			before := ex.lastSuccess
			want := "edgelab_source_up{job=\"edgelab\",instance=\"test\",role=\"hub\"} 0\n" + lastSuccess
			if got := scrapeMetrics(ex); got != want {
				t.Fatalf("failed scrape must retain only last-success timestamp, not cached or zero hub metrics:\ngot: %s\nwant: %s", got, want)
			}
			if ex.lastSuccess != before {
				t.Fatal("failed scrape advanced last-success timestamp")
			}
		})
	}
}
