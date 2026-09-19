package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// --- telegram ---------------------------------------------------------------

func TestTelegramBodyShapeAndSuccess(t *testing.T) {
	var mu sync.Mutex
	var got struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/botTOKEN123/sendMessage") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("content-type %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(b, &got)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := NewTelegram("TOKEN123", "chat-9")
	tg.APIBase = srv.URL
	if err := tg.Notify(context.Background(), Event{Kind: KindReleasePublished, Release: "app-v1", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if got.ChatID != "chat-9" || !strings.Contains(got.Text, "release-published") || !strings.Contains(got.Text, "release=app-v1") {
		t.Fatalf("body: %+v", got)
	}
}

func TestTelegramRetryOn500ThenSuccess(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		n := calls
		mu.Unlock()
		if n < 3 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg := NewTelegram("T", "c")
	tg.APIBase = srv.URL
	tg.maxAttempts = 5
	tg.sleep = func(time.Duration) {} // no real waiting in tests
	if err := tg.Notify(context.Background(), Event{Kind: KindDeliveryStaged, Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 3 {
		t.Fatalf("want 3 calls, got %d", calls)
	}
}

func TestSlackBodyShapeAndRetryAfter429(t *testing.T) {
	var mu sync.Mutex
	var bodies []string
	var sawRetryAfter429 bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		n := len(bodies)
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := NewSlack(srv.URL)
	s.maxAttempts = 3
	s.sleep = func(time.Duration) {}
	if err := s.Notify(context.Background(), Event{Kind: KindDeliveryLoaded, Detail: "device=edge-01", Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !sawRetryAfter429 && len(bodies) != 2 {
		t.Fatalf("want 2 attempts, got %d", len(bodies))
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(bodies[len(bodies)-1]), &parsed); err != nil {
		t.Fatal(err)
	}
	if _, ok := parsed["text"]; !ok {
		t.Fatalf("slack body missing text: %s", bodies[len(bodies)-1])
	}
}

func TestSlack429HonorsRetryAfterHeader(t *testing.T) {
	// With sleep captured, assert the sender waits at least Retry-After.
	var mu sync.Mutex
	var waits []time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := len(waits) + 1
		mu.Unlock()
		if n == 1 {
			w.Header().Set("Retry-After", "7")
			http.Error(w, "chill", http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	s := NewSlack(srv.URL)
	s.maxAttempts = 2
	s.sleep = func(d time.Duration) {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
	}
	if err := s.Notify(context.Background(), Event{Time: time.Now()}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(waits) != 1 || waits[0] < 7*time.Second {
		t.Fatalf("expected ~7s Retry-After wait, got %v", waits)
	}
}

func TestNonRetryable4xxFailsFast(t *testing.T) {
	var calls int
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		http.Error(w, "bad request", http.StatusBadRequest)
	}))
	defer srv.Close()
	tg := NewTelegram("T", "c")
	tg.APIBase = srv.URL
	tg.maxAttempts = 4
	tg.sleep = func(time.Duration) {}
	err := tg.Notify(context.Background(), Event{Time: time.Now()})
	mu.Lock()
	defer mu.Unlock()
	if err == nil {
		t.Fatal("expected error")
	}
	if calls != 1 {
		t.Fatalf("400 must not retry, got %d calls", calls)
	}
}

// --- dispatcher ----------------------------------------------------------------

type recordingNotifier struct {
	name  string
	mu    sync.Mutex
	seen  []Event
	delay time.Duration
}

func (r *recordingNotifier) Name() string { return r.name }

func (r *recordingNotifier) Notify(_ context.Context, e Event) error {
	if r.delay > 0 {
		time.Sleep(r.delay)
	}
	r.mu.Lock()
	r.seen = append(r.seen, e)
	r.mu.Unlock()
	return nil
}

func TestDispatcherFanOutIsolationAndNonBlocking(t *testing.T) {
	slow := &recordingNotifier{name: "slow", delay: 200 * time.Millisecond}
	fast := &recordingNotifier{name: "fast"}
	d := NewDispatcher(4, slow, fast)
	defer d.Close()

	start := time.Now()
	for i := 0; i < 3; i++ {
		if !d.Dispatch(Event{Kind: KindReleasePublished, Detail: fmt.Sprintf("n=%d", i), Time: time.Now()}) {
			t.Fatalf("event %d dropped", i)
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Fatalf("Dispatch blocked for %v", elapsed)
	}
	d.Close()
	if len(fast.seen) != 3 {
		t.Fatalf("fast got %d", len(fast.seen))
	}
	if len(slow.seen) != 3 {
		t.Fatalf("slow got %d", len(slow.seen))
	}
	stats := d.Stats()
	if stats["fast"] != "sent=3 failed=0 dropped=0" {
		t.Fatalf("fast stats %q", stats["fast"])
	}
}

func TestDispatcherDropsWhenQueueFullAndCounts(t *testing.T) {
	blocker := &recordingNotifier{name: "blocker", delay: 100 * time.Millisecond}
	d := NewDispatcher(1, blocker)
	// Fill the queue: first is picked up immediately (consumer sleeps), second
	// fills the slot, third must drop.
	d.Dispatch(Event{Kind: KindDeliveryStaged, Time: time.Now()})
	time.Sleep(10 * time.Millisecond) // let consumer take #1
	d.Dispatch(Event{Time: time.Now()})
	ok := d.Dispatch(Event{Time: time.Now()})
	if ok {
		t.Fatal("expected drop when queue full")
	}
	d.Close()
	if s := d.Stats()["blocker"]; !strings.Contains(s, "dropped=1") {
		t.Fatalf("stats %q", s)
	}
}

func TestBuildSkipsUnsetEnvAndKeepsGoing(t *testing.T) {
	old := LookupEnv
	defer func() { LookupEnv = old }()
	LookupEnv = func(name string) (string, bool) {
		if name == "SET_TOK" {
			return "tok", true
		}
		return "", false
	}
	ns, notes := Build([]NotifierConfig{
		{Type: "telegram", TokenEnv: "SET_TOK", ChatID: "c"},
		{Type: "slack", WebhookEnv: "UNSET_HOOK"},
		{Type: "carrier-pigeon"},
	})
	if len(ns) != 1 || ns[0].Name() != "telegram" {
		t.Fatalf("notifiers: %v", ns)
	}
	if len(notes) != 2 {
		t.Fatalf("notes: %v", notes)
	}
}
