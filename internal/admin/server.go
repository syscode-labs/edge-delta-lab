package admin

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"example.com/edge-delta-lab/internal/hub"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/registry"
)

// WatchStatusEntry aliases registry's watcher status type so the socket layer
// and cmd wiring share one shape without admin redefining it.
type WatchStatusEntry = registry.WatchStatusEntry

// methodHandler answers one admin method. params carries the request params.
type methodHandler func(params json.RawMessage) (any, *methodError)

type methodError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Server is the unix-socket NDJSON admin endpoint. It observes the serving
// process; it holds no authority over delivery. Long-polling is intentionally
// not supported: the TUI polls `stats` and `events` on a ticker, keeping the
// socket protocol trivially scriptable with plain shell (echo | nc -U).
type Server struct {
	log      *EventLog
	stats    func() any
	watch    func() []WatchStatusEntry
	reactor  func(ctx context.Context)
	handlers map[string]methodHandler

	ln      net.Listener
	mu      sync.Mutex
	clients int
	started time.Time
	closed  bool
}

// ServerOptions wires the admin server to live process state. All callbacks
// are optional; nil stats/watch just returns empty results.
type ServerOptions struct {
	// Log is the durable event log backing the events method.
	Log *EventLog
	// Stats returns the aggregate /stats snapshot (origin engine + hub).
	Stats func() any
	// Watch returns current registry watcher digest state.
	Watch func() []WatchStatusEntry
	// Reactor, when set, runs until ctx is done (e.g. a notify poller) so a
	// bare `serve --admin-socket` process stays alive and reactive.
	Reactor func(ctx context.Context)
}

// NewServer builds an admin server over opts.
func NewServer(opts ServerOptions) *Server {
	s := &Server{log: opts.Log, stats: opts.Stats, watch: opts.Watch, reactor: opts.Reactor, started: time.Now()}
	s.handlers = map[string]methodHandler{
		"ping": s.handlePing,
		"stats": func(json.RawMessage) (any, *methodError) {
			if s.stats == nil {
				return map[string]any{}, nil
			}
			return s.stats(), nil
		},
		"clients": s.handleClients,
		"receipts": func(params json.RawMessage) (any, *methodError) {
			var p struct {
				Dir string `json:"dir"`
			}
			if len(params) > 0 {
				_ = json.Unmarshal(params, &p)
			}
			rs, err := listReceipts(p.Dir)
			if err != nil {
				return nil, &methodError{Code: "receipts", Message: err.Error()}
			}
			return rs, nil
		},
		"events":    s.handleEvents,
		"watch-status": func(json.RawMessage) (any, *methodError) {
			out := []WatchStatusEntry{}
			if s.watch != nil {
				out = s.watch()
			}
			return out, nil
		},
	}
	return s
}

// Serve listens at socketPath (unlinking any stale socket first) and answers
// NDJSON requests until ctx is done or Close. The socket file is removed on
// clean shutdown.
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o755); err != nil {
		return err
	}
	// Stale socket cleanup: a previous crash leaves a bound path that would
	// make net.Listen fail with "address already in use".
	if fi, err := os.Stat(socketPath); err == nil && fi.Mode()&os.ModeSocket != 0 {
		_ = os.Remove(socketPath)
	}
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		return fmt.Errorf("admin listen %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o660); err != nil {
		ln.Close()
		return err
	}
	s.mu.Lock()
	s.ln = ln
	s.mu.Unlock()

	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
		case <-done:
		}
		ln.Close()
	}()

	if s.reactor != nil {
		go func() {
			rctx, cancel := context.WithCancel(ctx)
			defer cancel()
			s.reactor(rctx)
		}()
	}
	var wg sync.WaitGroup
	for {
		c, err := ln.Accept()
		if err != nil {
			close(done)
			wg.Wait()
			s.mu.Lock()
			already := s.closed
			s.closed = true
			s.ln = nil
			s.mu.Unlock()
			if !already {
				_ = os.Remove(socketPath)
			}
			if ctx.Err() != nil || already {
				return nil
			}
			return err
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.handleConn(ctx, c)
		}()
	}
}

// Close stops the listener. Serve then removes the socket and returns.
func (s *Server) Close() {
	s.mu.Lock()
	ln := s.ln
	s.closed = true
	s.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

func (s *Server) handleConn(ctx context.Context, c net.Conn) {
	defer c.Close()
	s.mu.Lock()
	s.clients++
	n := s.clients
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.clients--
		s.mu.Unlock()
	}()
	_ = n
	// One request per Readline; responses preserve the caller's id. Requests
	// may also arrive as a single line "id method" without params.
	sc := bufio.NewScanner(c)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	type req struct {
		ID     any              `json:"id"`
		Method string           `json:"method"`
		Params json.RawMessage  `json:"params"`
	}
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var r req
		if err := json.Unmarshal(line, &r); err != nil || r.Method == "" {
			_ = writeResp(c, nil, nil, &methodError{Code: "bad_request", Message: fmt.Sprintf("malformed request: %v", err)})
			continue
		}
		h, ok := s.handlers[r.Method]
		if !ok {
			_ = writeResp(c, r.ID, nil, &methodError{Code: "unknown_method", Message: r.Method})
			continue
		}
		if err := ctx.Err(); err != nil {
			return
		}
		result, merr := h(r.Params)
		_ = writeResp(c, r.ID, result, merr)
	}
}

func writeResp(w net.Conn, id any, result any, merr *methodError) error {
	if merr == nil && result == nil {
		result = map[string]any{}
	}
	b, err := json.Marshal(map[string]any{
		"id":     id,
		"ok":     merr == nil,
		"result": result,
		"error":  merr,
	})
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}

func (s *Server) handlePing(json.RawMessage) (any, *methodError) {
	return map[string]any{"pong": true, "uptime_seconds": int64(time.Since(s.started).Seconds())}, nil
}

// handleClients answers both forms: "clients" (no params) for connection
// metrics and "clients" with a receipts dir when the caller wants devices.
func (s *Server) handleClients(params json.RawMessage) (any, *methodError) {
	var p struct {
		ReceiptsDir string `json:"receipts_dir"`
	}
	if len(params) > 0 {
		_ = json.Unmarshal(params, &p)
	}
	out := map[string]any{"admin_connections": s.clientCount()}
	if p.ReceiptsDir != "" {
		rs, err := listReceipts(p.ReceiptsDir)
		if err != nil {
			return nil, &methodError{Code: "receipts", Message: err.Error()}
		}
		out["receipts"] = rs
	}
	return out, nil
}

func (s *Server) clientCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clients
}

func (s *Server) handleEvents(params json.RawMessage) (any, *methodError) {
	var p struct {
		After int64 `json:"after"`
		Limit int   `json:"limit"`
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &methodError{Code: "bad_params", Message: err.Error()}
		}
	}
	if s.log == nil {
		return map[string]any{"events": []Event{}, "oldest": 0, "latest": 0}, nil
	}
	evs, oldest, err := s.log.Query(p.After, p.Limit)
	if err != nil {
		return nil, &methodError{Code: "events", Message: err.Error()}
	}
	latest := int64(0)
	if n := len(evs); n > 0 {
		latest = evs[n-1].Seq
	}
	return map[string]any{"events": evs, "oldest": oldest, "latest": latest}, nil
}

// listReceipts reads and validates persisted sender receipts, mirroring the
// /receipts HTTP handler.
func listReceipts(dir string) ([]lab.Receipt, error) {
	out := []lab.Receipt{}
	if dir == "" {
		return out, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var r lab.Receipt
		if json.Unmarshal(b, &r) != nil || r.Validate() != nil {
			return nil, fmt.Errorf("corrupt persisted receipt %s", entry.Name())
		}
		out = append(out, r)
	}
	return out, nil
}

// HubStats converts a hub Stats snapshot into the generic any shape.
func HubStats(h *hub.Hub) any { return h.Snapshot() }
