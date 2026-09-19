// Package push implements the v3 hub announce channel: an in-process broker
// fed by publish/promote triggers, a /events websocket handler that fans
// announcements out to connected spokes, and a client dialer with exponential
// backoff reconnect. Announcements are hints only: they carry the hub's locally
// stored signed envelope bytes, and every spoke re-verifies the signature
// against its pinned public key before acting, so forged or unknown
// announcements can never change device state.
package push

import (
	"context"
	"encoding/json"
	"math/rand"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// Announce is one publish/promote notification. Envelope is the exact signed
// release JSON read from the hub's own origin directory (never attacker
// supplied bytes); spokes verify it independently.
type Announce struct {
	Kind     string `json:"kind"` // "publish" or "promote"
	Release  string `json:"release"`
	Sequence uint64 `json:"sequence"`
	Digest   string `json:"digest"` // sha256 hex of the envelope bytes
	Envelope []byte `json:"envelope"`
}

const subBuffer = 32

// Broker fans announcements out to subscribers without ever blocking the
// publisher. Slow subscribers drop announcements; spokes reconcile by polling
// regardless, so a dropped hint is never a correctness problem.
type Broker struct {
	mu sync.RWMutex
	subs map[*sub]struct{}
	// dropped/announced are atomic so concurrent announce writers and
	// Snapshot readers never race (the mutex guards only the sub set).
	dropped   atomic.Int64
	announced atomic.Int64
}

type sub struct {
	ch chan Announce
}

func NewBroker() *Broker { return &Broker{subs: map[*sub]struct{}{}} }

// Announce delivers one announcement to every subscriber. Non-blocking: if a
// subscriber's buffer is full its announcement is dropped for that subscriber.
func (b *Broker) Announce(a Announce) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	b.announced.Add(1)
	for s := range b.subs {
		select {
		case s.ch <- a:
		default:
			b.dropped.Add(1)
		}
	}
}

// Subscribe registers a subscriber and returns its channel plus a cancel func.
func (b *Broker) Subscribe(ctx context.Context) (<-chan Announce, func()) {
	s := &sub{ch: make(chan Announce, subBuffer)}
	b.mu.Lock()
	b.subs[s] = struct{}{}
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		delete(b.subs, s)
		b.mu.Unlock()
	}
	go func() {
		<-ctx.Done()
		cancel()
	}()
	return s.ch, cancel
}

// Count returns the number of live subscribers.
func (b *Broker) Count() int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs)
}

// Stats is a point-in-time broker snapshot for the admin socket.
type Stats struct {
	Subscribers int64 `json:"subscribers"`
	Announced   int64 `json:"announced"`
	Dropped     int64 `json:"dropped_slow_subscriber"`
}

func (b *Broker) Snapshot() Stats {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return Stats{Subscribers: int64(len(b.subs)), Announced: b.announced.Load(), Dropped: b.dropped.Load()}
}

const (
	writeWait  = 10 * time.Second
	pongWait   = 60 * time.Second
	pingPeriod = 20 * time.Second
	maxMessage = 4096
)

// Handler returns the /events websocket endpoint. Plain HTTP requests are
// answered 426 Upgrade Required so misconfigured pollers fail loudly. Origin
// checks are disabled: this is an explicitly lab-scoped endpoint.
func (b *Broker) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if lower(r.Header.Get("Upgrade")) != "websocket" {
			http.Error(w, "websocket required", http.StatusUpgradeRequired)
			return
		}
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serveConn(r.Context(), b, conn)
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

func serveConn(ctx context.Context, b *Broker, conn *websocket.Conn) {
	defer conn.Close()
	ch, cancel := b.Subscribe(ctx)
	defer cancel()

	// Reader: announcements are server-push only; inbound messages are
	// discarded but must be drained to process control frames and detect
	// disconnects.
	conn.SetReadLimit(maxMessage)
	conn.SetReadDeadline(time.Now().Add(pongWait))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(pongWait))
	})
	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	tick := time.NewTicker(pingPeriod)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage,
				websocket.FormatCloseMessage(websocket.CloseGoingAway, ""), time.Now().Add(writeWait))
			return
		case err := <-readErr:
			_ = err
			return
		case <-tick.C:
			_ = conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(writeWait))
		case a := <-ch:
			_ = conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := conn.WriteJSON(a); err != nil {
				return
			}
		}
	}
}

// ClientOptions configures Run.
type ClientOptions struct {
	URL        string // ws:// or wss:// /events endpoint
	Device     string // optional X-Edgelab-Device identification
	OnAnnounce func(Announce)
	Backoff    time.Duration // initial reconnect delay (default 1s)
	MaxBackoff time.Duration // reconnect cap (default 30s)
	DialTimeout time.Duration // handshake timeout (default 5s)
}

// Run dials the events endpoint and invokes OnAnnounce for every announcement
// until ctx ends, reconnecting with exponential backoff (+/- jitter) whenever
// the connection drops. Blocking; returns when ctx is done.
func Run(ctx context.Context, o ClientOptions) {
	if o.Backoff <= 0 {
		o.Backoff = time.Second
	}
	if o.MaxBackoff < o.Backoff {
		o.MaxBackoff = 30 * time.Second
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 5 * time.Second
	}
	backoff := o.Backoff
	for ctx.Err() == nil {
		d := websocket.Dialer{HandshakeTimeout: o.DialTimeout}
		hdr := http.Header{}
		if o.Device != "" {
			hdr.Set("X-Edgelab-Device", o.Device)
		}
		conn, _, err := d.DialContext(ctx, o.URL, hdr)
		if err != nil {
			if !sleep(ctx, jitter(backoff)) {
				return
			}
			backoff = minDuration(backoff*2, o.MaxBackoff)
			continue
		}
		backoff = o.Backoff
		conn.SetPingHandler(func(string) error {
			_ = conn.SetReadDeadline(time.Now().Add(pongWait))
			return conn.WriteControl(websocket.PongMessage, nil, time.Now().Add(writeWait))
		})
		for ctx.Err() == nil {
			_ = conn.SetReadDeadline(time.Now().Add(pongWait))
			_, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			var a Announce
			if json.Unmarshal(msg, &a) == nil && o.OnAnnounce != nil {
				o.OnAnnounce(a)
			}
		}
		_ = conn.Close()
		if ctx.Err() != nil {
			return
		}
		if !sleep(ctx, jitter(backoff)) {
			return
		}
		backoff = minDuration(backoff*2, o.MaxBackoff)
	}
}

func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return d
	}
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(d-half)+1))
}

func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
