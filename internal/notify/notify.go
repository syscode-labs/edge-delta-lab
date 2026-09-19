// Package notify implements the notifier interface, Telegram/Slack senders
// with retry/backoff, and a non-blocking fan-out dispatcher. Per the v3 brief
// amendment, live Telegram/Slack delivery is NOT exercised: senders are
// unit-tested against httptest fakes only; credentials come from env vars
// supplied later.
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Event kinds emitted by the v3 pipeline.
const (
	KindReleasePublished = "release-published"
	KindDeliveryStaged   = "delivery-staged"
	KindDeliveryLoaded   = "delivery-loaded"
	KindDeliveryFailed   = "delivery-failed"
	KindWatcherError     = "watcher-error"
)

// Event is a single notification-worthy occurrence.
type Event struct {
	Kind    string    `json:"kind"`
	Repo    string    `json:"repo,omitempty"`
	Tag     string    `json:"tag,omitempty"`
	Digest  string    `json:"digest,omitempty"`
	Release string    `json:"release,omitempty"`
	Detail  string    `json:"detail,omitempty"`
	Time    time.Time `json:"time"`
}

// Text renders the plain-text form sent to Telegram/Slack.
func (e Event) Text() string {
	var b strings.Builder
	b.WriteString("edgelab ")
	b.WriteString(e.Kind)
	parts := []string{}
	if e.Release != "" {
		parts = append(parts, "release="+e.Release)
	}
	if e.Repo != "" {
		parts = append(parts, "repo="+e.Repo)
	}
	if e.Tag != "" {
		parts = append(parts, "tag="+e.Tag)
	}
	if e.Digest != "" {
		parts = append(parts, "digest="+e.Digest)
	}
	if e.Detail != "" {
		parts = append(parts, e.Detail)
	}
	if len(parts) > 0 {
		b.WriteString(": ")
		b.WriteString(strings.Join(parts, " "))
	}
	return b.String()
}

// Notifier delivers events to one destination.
type Notifier interface {
	Name() string
	Notify(ctx context.Context, e Event) error
}

// --- sender plumbing -------------------------------------------------------

const defaultMaxAttempts = 4

// httpSender carries the shared POST/retry machinery.
type httpSender struct {
	client      *http.Client
	maxAttempts int
	// sleep is injectable for tests; zero means time.Sleep.
	sleep func(time.Duration)
	// rand for backoff jitter; injectable seed not needed (jitter only).
}

func (s *httpSender) attempt(ctx context.Context, e Event, name string, build func() (*http.Request, error)) error {
	max := s.maxAttempts
	if max <= 0 {
		max = defaultMaxAttempts
	}
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < max; attempt++ {
		if attempt > 0 {
			d := backoff + time.Duration(rand.Int63n(int64(backoff/2)+1)) // jitter
			if s.sleep != nil {
				s.sleep(d)
			} else {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(d):
				}
			}
			backoff *= 2
		}
		req, err := build()
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", name, err)
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		resp.Body.Close()
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			lastErr = fmt.Errorf("%s: 429 %s", name, strings.TrimSpace(string(body)))
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, err := strconv.Atoi(ra); err == nil && secs >= 0 {
					backoff = time.Duration(secs) * time.Second
				}
			}
		case resp.StatusCode >= 500:
			lastErr = fmt.Errorf("%s: %s %s", name, resp.Status, strings.TrimSpace(string(body)))
		default:
			return fmt.Errorf("%s: %s %s", name, resp.Status, strings.TrimSpace(string(body)))
		}
	}
	return lastErr
}

// --- Telegram --------------------------------------------------------------

const telegramAPIBase = "https://api.telegram.org"

// Telegram sends plain text via the Telegram bot API sendMessage.
type Telegram struct {
	APIBase string // override for httptest fakes; default api.telegram.org
	Token   string
	ChatID  string
	httpSender
}

// NewTelegram builds a Telegram notifier. token should come from an env var.
func NewTelegram(token, chatID string) *Telegram {
	return &Telegram{APIBase: telegramAPIBase, Token: token, ChatID: chatID,
		httpSender: httpSender{client: &http.Client{Timeout: 15 * time.Second}}}
}

func (t *Telegram) Name() string { return "telegram" }

func (t *Telegram) Notify(ctx context.Context, e Event) error {
	return t.attempt(ctx, e, t.Name(), func() (*http.Request, error) {
		u := t.APIBase + "/bot" + t.Token + "/sendMessage"
		body, err := json.Marshal(map[string]string{"chat_id": t.ChatID, "text": e.Text()})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// --- Slack ------------------------------------------------------------------

// Slack posts {"text": ...} to an incoming webhook URL.
type Slack struct {
	Webhook string
	httpSender
}

// NewSlack builds a Slack notifier. webhook should come from an env var.
func NewSlack(webhook string) *Slack {
	return &Slack{Webhook: webhook,
		httpSender: httpSender{client: &http.Client{Timeout: 15 * time.Second}}}
}

func (s *Slack) Name() string { return "slack" }

func (s *Slack) Notify(ctx context.Context, e Event) error {
	if _, err := url.Parse(s.Webhook); err != nil {
		return fmt.Errorf("slack webhook: %w", err)
	}
	return s.attempt(ctx, e, s.Name(), func() (*http.Request, error) {
		body, err := json.Marshal(map[string]string{"text": e.Text()})
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.Webhook, strings.NewReader(string(body)))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		return req, nil
	})
}

// --- dispatcher --------------------------------------------------------------

// Dispatcher fans every event out to ALL notifiers without ever blocking the
// caller: each notifier has its own bounded queue; when one is full that
// notifier's event is dropped and counted (other notifiers still get it).
// Retry semantics are owned by the notifier itself.
type Dispatcher struct {
	mu        sync.Mutex
	queues    []*notifierQueue
	dropped   sync.Map // notifier name -> uint64
	sent      sync.Map // notifier name -> uint64
	failed    sync.Map // notifier name -> uint64
	wg        sync.WaitGroup
	closed    bool
}

type notifierQueue struct {
	n  Notifier
	ch chan Event
}

// NewDispatcher builds a dispatcher over notifiers with the given per-notifier
// queue depth.
func NewDispatcher(queueDepth int, notifiers ...Notifier) *Dispatcher {
	if queueDepth <= 0 {
		queueDepth = 64
	}
	d := &Dispatcher{}
	for _, n := range notifiers {
		d.queues = append(d.queues, &notifierQueue{n: n, ch: make(chan Event, queueDepth)})
		d.wg.Add(1)
		go d.consume(n, d.queues[len(d.queues)-1].ch)
	}
	return d
}

// Dispatch enqueues e for every notifier without blocking; returns false when
// at least one notifier's queue was full and the event was dropped for it.
func (d *Dispatcher) Dispatch(e Event) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || len(d.queues) == 0 {
		return true
	}
	ok := true
	for _, q := range d.queues {
		select {
		case q.ch <- e:
		default:
			bump(&d.dropped, q.n.Name())
			ok = false
		}
	}
	return ok
}

func (d *Dispatcher) consume(n Notifier, ch <-chan Event) {
	defer d.wg.Done()
	for e := range ch {
		if err := n.Notify(context.Background(), e); err != nil {
			bump(&d.failed, n.Name())
		} else {
			bump(&d.sent, n.Name())
		}
	}
}

// Close stops the dispatcher after draining the queues. Idempotent.
func (d *Dispatcher) Close() {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return
	}
	d.closed = true
	for _, q := range d.queues {
		close(q.ch)
	}
	d.mu.Unlock()
	d.wg.Wait()
}

// Stats snapshots per-notifier counters.
func (d *Dispatcher) Stats() map[string]string {
	out := map[string]string{}
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, q := range d.queues {
		name := q.n.Name()
		out[name] = fmt.Sprintf("sent=%d failed=%d dropped=%d",
			loadCounter(&d.sent, name), loadCounter(&d.failed, name), loadCounter(&d.dropped, name))
	}
	return out
}

func loadCounter(m *sync.Map, k string) uint64 {
	v, _ := m.Load(k)
	c, _ := v.(uint64)
	return c
}

func bump(m *sync.Map, k string) {
	for {
		v, loaded := m.LoadOrStore(k, uint64(1))
		if !loaded {
			return
		}
		c := v.(uint64)
		if m.CompareAndSwap(k, c, c+1) {
			return
		}
	}
}

// --- config ------------------------------------------------------------------

// NotifierConfig is one entry of a `notifiers:` list in a YAML config.
// Credentials are never inline: they are read from the named env var.
type NotifierConfig struct {
	Type       string `yaml:"type"` // telegram | slack
	TokenEnv   string `yaml:"token_env,omitempty"`   // telegram bot token env var
	ChatID     string `yaml:"chat_id,omitempty"`     // telegram chat id
	WebhookEnv string `yaml:"webhook_env,omitempty"` // slack webhook env var
}

// ErrMissingEnv is returned when a configured env var is unset and required.
var ErrMissingEnv = errors.New("notification credential env var is not set")

// LookupEnv is the env indirection (injectable in tests).
var LookupEnv = func(name string) (string, bool) { return lookupEnv(name) }

// Build constructs notifiers from config entries, resolving credentials via
// env vars. Entries whose env var is unset are skipped (with a note) rather
// than failing startup — notification loss is never worth blocking delivery.
func Build(cfgs []NotifierConfig) ([]Notifier, []string) {
	var out []Notifier
	var notes []string
	for i, c := range cfgs {
		switch strings.ToLower(c.Type) {
		case "telegram":
			tok, ok := LookupEnv(c.TokenEnv)
			if !ok || tok == "" {
				notes = append(notes, fmt.Sprintf("notifiers[%d]: %s unset, telegram disabled", i, c.TokenEnv))
				continue
			}
			out = append(out, NewTelegram(tok, c.ChatID))
		case "slack":
			wh, ok := LookupEnv(c.WebhookEnv)
			if !ok || wh == "" {
				notes = append(notes, fmt.Sprintf("notifiers[%d]: %s unset, slack disabled", i, c.WebhookEnv))
				continue
			}
			out = append(out, NewSlack(wh))
		default:
			notes = append(notes, fmt.Sprintf("notifiers[%d]: unknown type %q skipped", i, c.Type))
		}
	}
	return out, notes
}

func lookupEnv(name string) (string, bool) { return os.LookupEnv(name) }
