package admin
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"example.com/edge-delta-lab/internal/lab"
)

// startServer builds a Server with an event log in dir and serves it on a
// fresh unix socket. Returns the socket path, the log and a stop func.
func startServer(t *testing.T, dir string, maxBytes int64) (string, *EventLog, func()) {
	t.Helper()
	logPath := filepath.Join(dir, "events.jsonl")
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	lg, err := OpenEventLog(logPath, maxBytes)
	if err != nil {
		t.Fatalf("open event log: %v", err)
	}
	srv := NewServer(ServerOptions{Log: lg})
	sock := filepath.Join(dir, "admin.sock")
	ctx, cancel := context.WithCancel(context.Background())
	serr := make(chan error, 1)
	go func() { serr <- srv.Serve(ctx, sock) }()
	stop := func() {
		cancel()
		srv.Close()
		select {
		case <-serr:
		case <-time.After(5 * time.Second):
			t.Log("admin server did not stop in time")
		}
		_ = os.Remove(sock)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, err := net.Dial("unix", sock); err == nil {
			c.Close()
			break
		}
		if time.Now().After(deadline) {
			stop()
			t.Fatal("admin socket never became ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return sock, lg, stop
}

type rpcError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// rpc performs one NDJSON request and decodes the response envelope.
func rpc(t *testing.T, sock, line string) (ok bool, result json.RawMessage, rerr *rpcError) {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte(line + "\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	respLine, err := bufio.NewReader(c).ReadString('\n')
	if err != nil {
		t.Fatalf("read response: %v (sent %q)", err, line)
	}
	var resp struct {
		ID     int             `json:"id"`
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  *rpcError       `json:"error"`
	}
	if err := json.Unmarshal([]byte(respLine), &resp); err != nil {
		t.Fatalf("decode response %q: %v", respLine, err)
	}
	return resp.OK, resp.Result, resp.Error
}

func mustResult(t *testing.T, sock, method, params string) json.RawMessage {
	t.Helper()
	line := fmt.Sprintf(`{"id":1,"method":%q}`, method)
	if params != "" {
		line = fmt.Sprintf(`{"id":1,"method":%q,"params":%s}`, method, params)
	}
	ok, res, rerr := rpc(t, sock, line)
	if !ok {
		t.Fatalf("%s: unexpected error %v", method, rerr)
	}
	return res
}

func TestSocketRoundtrip(t *testing.T) {
	dir := t.TempDir()
	sock, lg, stop := startServer(t, dir, 0)
	defer stop()
	if err := lg.Emit("test-kind", "hello"); err != nil {
		t.Fatalf("emit: %v", err)
	}
	// ping
	var pong map[string]any
	if err := json.Unmarshal(mustResult(t, sock, "ping", ""), &pong); err != nil {
		t.Fatal(err)
	}
	if pong["pong"] != true {
		t.Fatalf("ping result: %v", pong)
	}
	// stats with nil sources still well-formed
	ok, _, rerr := rpc(t, sock, `{"id":2,"method":"stats"}`)
	if !ok || rerr != nil {
		t.Fatalf("stats: ok=%v err=%v", ok, rerr)
	}
	// events returns the emitted event
	res := mustResult(t, sock, "events", `{"after":0,"limit":10}`)
	var er struct {
		Events []Event `json:"events"`
		Oldest int64   `json:"oldest"`
		Latest int64   `json:"latest"`
	}
	if err := json.Unmarshal(res, &er); err != nil {
		t.Fatal(err)
	}
	if len(er.Events) != 1 || er.Events[0].Kind != "test-kind" || er.Events[0].Seq != 1 {
		t.Fatalf("events: %+v", er)
	}
	// unknown method
	ok, _, rerr = rpc(t, sock, `{"id":3,"method":"no-such-method"}`)
	if ok || rerr == nil || rerr.Code != "unknown_method" {
		t.Fatalf("unknown method: ok=%v err=%+v", ok, rerr)
	}
	// malformed line still gets one error response, connection stays scoped
	ok, _, rerr = rpc(t, sock, `{this is not json`)
	if ok || rerr == nil || rerr.Code != "bad_request" {
		t.Fatalf("malformed: ok=%v err=%+v", ok, rerr)
	}
	// socket file permissions should be group-readable (0660 family)
	st, err := os.Stat(sock)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm()&0o060 == 0 {
		t.Fatalf("socket perms too strict: %v", st.Mode().Perm())
	}
}

func TestReceiptsListing(t *testing.T) {
	dir := t.TempDir()
	sock, _, stop := startServer(t, dir, 0)
	defer stop()
	good := lab.Receipt{
		DeviceID: "dev-1", Release: "rel-1", Phase: "staged",
		ArtifactSHA256: strings.Repeat("ab", 32),
		RecordedAt:     time.Now().UTC().Format(time.RFC3339Nano),
	}
	good.ID = receiptIDForTest(t, good)
	// Corrupt receipt: bad ID (validation must fail and be surfaced).
	bad := good
	bad.ID = "forged"
	if err := os.WriteFile(filepath.Join(dir, "receipt-bad.json"), mustJSON(t, bad), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "receipt-good.json"), mustJSON(t, good), 0o640); err != nil {
		t.Fatal(err)
	}
	ok, _, rerr := rpc(t, sock, fmt.Sprintf(`{"id":1,"method":"clients","params":{"receipts_dir":%q}}`, dir))
	if ok || rerr == nil || rerr.Code != "receipts" {
		t.Fatalf("corrupt receipt should fail listing: ok=%v err=%+v", ok, rerr)
	}
	// Remove the corrupt one; listing must now succeed with exactly one receipt.
	if err := os.Remove(filepath.Join(dir, "receipt-bad.json")); err != nil {
		t.Fatal(err)
	}
	res := mustResult(t, sock, "clients", fmt.Sprintf(`{"receipts_dir":%q}`, dir))
	var cr struct {
		Receipts []lab.Receipt `json:"receipts"`
	}
	if err := json.Unmarshal(res, &cr); err != nil {
		t.Fatal(err)
	}
	if len(cr.Receipts) != 1 || cr.Receipts[0].DeviceID != "dev-1" {
		t.Fatalf("receipts: %+v", cr.Receipts)
	}
	// Missing dir yields an empty listing, not an error (observation plane
	// stays quiet about absent state).
	res2 := mustResult(t, sock, "clients", `{"receipts_dir":"/nonexistent/dir"}`)
	var cr2 struct {
		Receipts []lab.Receipt `json:"receipts"`
	}
	if err := json.Unmarshal(res2, &cr2); err != nil {
		t.Fatal(err)
	}
	if len(cr2.Receipts) != 0 {
		t.Fatalf("missing dir should list zero receipts: %+v", cr2.Receipts)
	}
}

func TestEventLogRotationAndRestart(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "events.jsonl")
	// Tiny cap: a couple of events must push generation 0 into events.jsonl.1.
	lg, err := OpenEventLog(logPath, 220)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := lg.Emit("rotate-test", fmt.Sprintf("event-%d", i)); err != nil {
			t.Fatalf("emit %d: %v", i, err)
		}
	}
	if _, err := os.Stat(logPath + ".1"); err != nil {
		t.Fatalf("rotated generation missing: %v", err)
	}
	evs, oldest, err := lg.Query(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 4 || evs[0].Seq != 1 || evs[3].Seq != 4 || oldest != 1 {
		t.Fatalf("query across rotation: n=%d oldest=%d first=%+v", len(evs), oldest, evs[0])
	}
	// Restart: close and reopen; seq continues, old events remain queryable.
	if err := lg.Close(); err != nil {
		t.Fatal(err)
	}
	lg2, err := OpenEventLog(logPath, 220)
	if err != nil {
		t.Fatal(err)
	}
	if err := lg2.Emit("rotate-test", "after-restart"); err != nil {
		t.Fatal(err)
	}
	evs, oldest, err = lg2.Query(0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 5 || evs[4].Seq != 5 || evs[4].Detail != "after-restart" || oldest != 1 {
		t.Fatalf("query after restart: n=%d oldest=%d last=%+v", len(evs), oldest, evs[len(evs)-1])
	}
	// after-cursor query keeps working across the restart boundary.
	evs, _, err = lg2.Query(3, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 2 || evs[0].Seq != 4 || evs[1].Seq != 5 {
		t.Fatalf("cursor query after restart: %+v", evs)
	}
	if err := lg2.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOnEmitHooks(t *testing.T) {
	dir := t.TempDir()
	lg, err := OpenEventLog(filepath.Join(dir, "events.jsonl"), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lg.Close()
	var got []Event
	done := make(chan struct{}, 8)
	lg.OnEmit(func(ev Event) {
		got = append(got, ev)
		select {
		case done <- struct{}{}:
		default:
		}
	})
	if err := lg.Emit("hook-test", "one"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("hook never fired")
	}
	if len(got) != 1 || got[0].Kind != "hook-test" || got[0].Seq != 1 {
		t.Fatalf("hook events: %+v", got)
	}
}

func TestCSVRendering(t *testing.T) {
	// Object: flattened key,value rows.
	obj := map[string]any{"a": 1, "nested": map[string]any{"b": "x,y"}}
	s, err := CSV(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(s, "a,1\n") || !strings.Contains(s, "nested.b,\"x,y\"") {
		t.Fatalf("object csv:\n%s", s)
	}
	// Array of objects: header plus one row per element.
	arr := []map[string]any{
		{"repo": "a", "tag": "1"},
		{"repo": "b", "tag": "2"},
	}
	s, err = CSV(arr)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], "repo") || !strings.Contains(lines[0], "tag") {
		t.Fatalf("array csv:\n%s", s)
	}
	// Errors on non-object arrays.
	if _, err = CSV([]string{"plain"}); err == nil {
		t.Fatal("expected error for scalar array")
	}
	if _, err = CSV(42); err == nil {
		t.Fatal("expected error for scalar")
	}
}

// helpers

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func receiptIDForTest(t *testing.T, r lab.Receipt) string {
	t.Helper()
	// Receipt IDs are the content hash over the receipt with ID cleared,
	// exactly as queueReceipt computes them.
	r.ID = ""
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	id := lab.Hash(b)
	r.ID = id
	if err := r.Validate(); err != nil {
		t.Fatalf("receipt validate: %v", err)
	}
	return id
}
