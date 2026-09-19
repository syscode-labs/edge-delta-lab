// Package admin implements the v3 stage-5 admin plane: a durable, size-capped
// JSONL event log and a unix-socket NDJSON request/response server exposing
// stats, clients, receipts, events and watch-status. Everything here is lab
// tooling: it observes the origin/hub process, it never changes delivery
// semantics.
package admin

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Event is one durable admin-plane event record.
type Event struct {
	Seq    int64  `json:"seq"`
	Time   string `json:"time"` // RFC3339Nano UTC
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// EventLog is a size-capped, fsynced JSONL event log. At most two files exist:
// path (current) and path+".1" (previous generation), so total disk use is
// bounded by 2*maxBytes. Sequence numbers are global and monotonic across
// rotations and restarts: they are recovered from the file tail on open, so a
// cursor-based query issued before a restart keeps working after it.
type EventLog struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	f        *os.File
	size     int64
	seq      int64
	closed   bool
	hooks    []func(Event)
}

// DefaultEventLogBytes caps the current generation at 8 MiB.
const DefaultEventLogBytes = 8 << 20

// OpenEventLog opens (creating if needed) the JSONL log at path and recovers
// the last sequence number from its tail.
func OpenEventLog(path string, maxBytes int64) (*EventLog, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultEventLogBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	l := &EventLog{path: path, maxBytes: maxBytes, f: f, size: st.Size()}
	l.seq, err = lastSeq(path)
	if err != nil {
		f.Close()
		return nil, err
	}
	if l.size >= maxBytes {
		if err := l.rotateLocked(); err != nil {
			f.Close()
			return nil, err
		}
	}
	return l, nil
}

// lastSeq scans the tail of the log for the last complete JSON line's seq.
func lastSeq(path string) (int64, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return 0, err
	}
	tail := int64(64 << 10)
	if st.Size() < tail {
		tail = st.Size()
	}
	if _, err := f.Seek(-tail, io.SeekEnd); err != nil {
		return 0, err
	}
	b := make([]byte, tail)
	if _, err := f.Read(b); err != nil && tail > 0 {
		return 0, err
	}
	var seq int64
	for _, line := range bytes.Split(b, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var ev Event
		if json.Unmarshal(line, &ev) == nil && ev.Seq > seq {
			seq = ev.Seq
		}
	}
	return seq, nil
}

func (l *EventLog) rotateLocked() error {
	if err := l.f.Close(); err != nil {
		return err
	}
	if err := os.Rename(l.path, l.path+".1"); err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	l.f = f
	l.size = 0
	return syncDir(filepath.Dir(l.path))
}

// OnEmit registers a hook that fires after each successful durable append.
// Hooks are observation only: they are invoked outside the log's mutex, after
// the event has been written and fsynced, and hook errors are ignored.
func (l *EventLog) OnEmit(hook func(Event)) {
	l.mu.Lock()
	l.hooks = append(l.hooks, hook)
	l.mu.Unlock()
}

// Emit appends one event durably (write + fsync). Rotates first when the
// current generation has reached maxBytes. Registered OnEmit hooks fire after
// a successful durable append, with the lock released, using copies of the
// hook list and event so hooks cannot block appends or mutate what they see.
func (l *EventLog) Emit(kind, detail string) error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return fmt.Errorf("event log closed")
	}
	if l.size >= l.maxBytes {
		if err := l.rotateLocked(); err != nil {
			l.mu.Unlock()
			return fmt.Errorf("rotate event log: %w", err)
		}
	}
	l.seq++
	ev := Event{Seq: l.seq, Time: time.Now().UTC().Format(time.RFC3339Nano), Kind: kind, Detail: detail}
	b, err := json.Marshal(ev)
	if err != nil {
		l.seq--
		l.mu.Unlock()
		return err
	}
	b = append(b, '\n')
	if _, err := l.f.Write(b); err != nil {
		l.seq--
		l.mu.Unlock()
		return err
	}
	if err := l.f.Sync(); err != nil {
		l.mu.Unlock()
		return err
	}
	l.size += int64(len(b))
	hooks := make([]func(Event), len(l.hooks))
	copy(hooks, l.hooks)
	l.mu.Unlock()
	for _, h := range hooks {
		h(ev)
	}
	return nil
}

// Query returns events with Seq > after (oldest first), capped at limit
// (default 200, max 1000). It scans the rotated generation first, then the
// current one. oldest is the smallest sequence still retained (0 when empty);
// if after < oldest-1 the requested window has been partially rotated away and
// the caller can detect the gap from oldest.
func (l *EventLog) Query(after int64, limit int) (events []Event, oldest int64, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	events = []Event{}
	paths := []string{}
	if l.path != "" {
		if _, e := os.Stat(l.path + ".1"); e == nil {
			paths = append(paths, l.path+".1")
		}
		paths = append(paths, l.path)
	}
	for _, p := range paths {
		f, e := os.Open(p)
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return events, oldest, e
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
		for sc.Scan() {
			line := bytes.TrimSpace(sc.Bytes())
			if len(line) == 0 {
				continue
			}
			var ev Event
			if json.Unmarshal(line, &ev) != nil {
				continue // torn tail write: skip
			}
			if oldest == 0 || ev.Seq < oldest {
				oldest = ev.Seq
			}
			if ev.Seq <= after {
				continue
			}
			events = append(events, ev)
			if len(events) >= limit {
				f.Close()
				return events, oldest, nil
			}
		}
		f.Close()
		if e := sc.Err(); e != nil {
			return events, oldest, e
		}
	}
	return events, oldest, nil
}

// Close releases the file handle. Idempotent.
func (l *EventLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return l.f.Close()
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// SortWatchStatus orders entries by repo then tag for stable output. The
// entry type is registry.WatchStatusEntry; the alias lives in server.go.
func SortWatchStatus(es []WatchStatusEntry) {
	sort.Slice(es, func(i, j int) bool {
		if es[i].Repo != es[j].Repo {
			return es[i].Repo < es[j].Repo
		}
		return es[i].Tag < es[j].Tag
	})
}

// CSV renders a result value as CSV. Array-of-object results become one row
// per element (header from the first element's keys); object results become
// key,value rows with nested keys flattened using dots.
func CSV(v any) (string, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var raw any
	if err := json.Unmarshal(b, &raw); err != nil {
		return "", err
	}
	var out strings.Builder
	switch t := raw.(type) {
	case []any:
		if len(t) == 0 {
			return "", nil
		}
		first, ok := t[0].(map[string]any)
		if !ok {
			return "", fmt.Errorf("csv: array elements must be objects")
		}
		keys := make([]string, 0, len(first))
		for k := range first {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		out.WriteString(csvRow(keys))
		for _, el := range t {
			m, ok := el.(map[string]any)
			if !ok {
				return "", fmt.Errorf("csv: array elements must be objects")
			}
			vals := make([]string, len(keys))
			for i, k := range keys {
				vals[i] = csvCell(m[k])
			}
			out.WriteString(csvRow(vals))
		}
	case map[string]any:
		flat := map[string]string{}
		flatten("", t, flat)
		keys := make([]string, 0, len(flat))
		for k := range flat {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out.WriteString(csvRow([]string{k, flat[k]}))
		}
	default:
		return "", fmt.Errorf("csv: result must be an object or array")
	}
	return out.String(), nil
}

func flatten(prefix string, m map[string]any, out map[string]string) {
	for k, v := range m {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			flatten(key, nested, out)
			continue
		}
		out[key] = csvCell(v)
	}
}

func csvCell(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	default:
		b, _ := json.Marshal(t)
		return string(b)
	}
}

func csvRow(cells []string) string {
	var b strings.Builder
	for i, c := range cells {
		if i > 0 {
			b.WriteByte(',')
		}
		if strings.ContainsAny(c, ",\"\n") {
			c = "\"" + strings.ReplaceAll(c, "\"", "\"\"") + "\""
		}
		b.WriteString(c)
	}
	b.WriteByte('\n')
	return b.String()
}
