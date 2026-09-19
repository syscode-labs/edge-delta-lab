package lab

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// A real ResponseWriter can accept fewer bytes than requested and return an
// error. Counters must use n, never len(p), file size, or the request count.
type partialResponseWriter struct {
	header  http.Header
	limit   int
	written int
}

func (w *partialResponseWriter) Header() http.Header { return w.header }
func (w *partialResponseWriter) WriteHeader(int)     {}
func (w *partialResponseWriter) Write(p []byte) (int, error) {
	n := min(len(p), w.limit-w.written)
	w.written += n
	if n < len(p) {
		return n, errors.New("injected short socket write")
	}
	return n, nil
}

func TestSenderRetryBodyCountsActualPartialWrites(t *testing.T) {
	root := t.TempDir()
	body := data(20000)
	rel := "chunks/ab/" + Hash(body) + ".gz"
	path := filepath.Join(root, filepath.FromSlash(rel))
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, body, 0600); e != nil {
		t.Fatal(e)
	}
	s := NewFaultServer(root, FaultPlan{})
	for i, limit := range []int{9000, 123, 20000} {
		w := &partialResponseWriter{header: http.Header{}, limit: limit}
		s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+rel, nil))
		if w.written != limit {
			t.Fatalf("attempt %d: wrote %d want %d", i+1, w.written, limit)
		}
	}
	stats := s.Snapshot()
	if stats.ChunkBodyBytes != 29123 || stats.ChunkRetryBodyBytes != 20123 {
		t.Fatalf("incorrect accepted-body counters: %+v", stats)
	}
	if stats.ChunkRequests != 3 || stats.ObjectRequests[rel] != 3 || stats.MetadataBodyBytes != 0 {
		t.Fatalf("existing totals changed: %+v", stats)
	}
}

func TestSenderRetryBodyAfterBodylessFailure(t *testing.T) {
	r := newRig(t, FaultPlan{})
	m := r.publish(t, "v1", 1, data(32768))
	c := Unique(m)[0]
	// No concurrent requests: the first request is a bodyless 503, followed
	// by a successful repeated request whose entire accepted body is counted.
	r.s.Plan.FailFirst = 1
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusOK} {
		w := httptest.NewRecorder()
		r.s.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/"+BlobRel(c), nil))
		if w.Code != status {
			t.Fatalf("status=%d want=%d", w.Code, status)
		}
	}
	stats := r.s.Snapshot()
	if stats.ChunkRetryBodyBytes != c.EncodedSize || stats.ChunkBodyBytes != c.EncodedSize {
		t.Fatalf("bodyless failure counted as bytes or retry omitted: %+v", stats)
	}
}
