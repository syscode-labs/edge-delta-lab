package lab

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTelemetrySnapshotIsBoundedAndProcessLifetime(t *testing.T) {
	tel := NewTelemetry()
	tel.chunkBytes.Store(123)
	tel.metadataBytes.Store(7)
	tel.chunkRequests.Store(2)
	tel.syncSuccess.Store(1)
	tel.verifiedChunks.Store(9)
	tel.reusedChunks.Store(3)
	tel.lastDownloadBytes.Store(123)
	tel.lastVerifiedChunks.Store(2)
	tel.lastReusedChunks.Store(7)
	tel.state.Store(clientLoaded)
	tel.lastSuccess.Store(time.Now().UnixNano())
	s := tel.Snapshot()
	if s["chunk_response_body_bytes"] != int64(123) || s["metadata_response_body_bytes"] != int64(7) || s["state"] != "loaded" {
		t.Fatalf("unexpected telemetry snapshot: %#v", s)
	}
	if s["verified_chunks"] != int64(9) || s["reused_chunks"] != int64(3) || s["last_sync_reused_chunks"] != int64(7) {
		t.Fatalf("missing delta telemetry: %#v", s)
	}
	if _, ok := s["devices"]; ok {
		t.Fatal("unbounded device map leaked into client telemetry")
	}
}

func TestReceiptLostResponseReplaysAfterSenderRestart(t *testing.T) {
	root := t.TempDir()
	s := NewFaultServer(root, FaultPlan{})
	s.ReceiptsDir = filepath.Join(root, "sender-receipts")
	o := DefaultAgentOptions()
	o.StateDir = filepath.Join(root, "edge")
	o.DeviceID = "test-edge"
	o.AllowHTTP = true
	if e := queueReceipt(o, Summary{Release: "r1", Sequence: 1, Phase: "staged", ArtifactSHA256: Hash([]byte("archive"))}); e != nil {
		t.Fatal(e)
	}
	entries, e := os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if e != nil || len(entries) != 1 {
		t.Fatalf("outbox: %v, %v", entries, e)
	}
	path := filepath.Join(o.StateDir, "outbox", entries[0].Name())
	body, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var receipt Receipt
	if e = json.Unmarshal(body, &receipt); e != nil || receipt.validate() != nil {
		t.Fatalf("invalid persisted outbox: %v", e)
	}

	// Persist through the real receipt handler, but drop the socket before its
	// successful status reaches the caller. No receipt is synthesized on replay.
	lost := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		saved := httptest.NewRecorder()
		s.ServeHTTP(saved, r)
		if saved.Code != http.StatusCreated {
			t.Errorf("persistence failed before response loss: %d", saved.Code)
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		_ = conn.Close()
	}))
	o.ReceiptURL = lost.URL + "/receipts"
	if e = FlushReceipts(context.Background(), o); e == nil {
		t.Fatal("lost response reported success")
	}
	lost.Close()
	retained, e := os.ReadFile(path)
	if e != nil || !bytes.Equal(retained, body) {
		t.Fatalf("lost response changed outbox: %v", e)
	}
	persisted, e := os.ReadFile(filepath.Join(s.ReceiptsDir, receipt.ID+".json"))
	if e != nil || !bytes.Equal(persisted, body) {
		t.Fatalf("sender had not persisted before response loss: %v", e)
	}

	restarted := NewFaultServer(root, FaultPlan{})
	restarted.ReceiptsDir = s.ReceiptsDir
	server := httptest.NewServer(restarted)
	defer server.Close()
	o.ReceiptURL = server.URL + "/receipts"
	if e = FlushReceipts(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	entries, e = os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if e != nil || len(entries) != 0 {
		t.Fatalf("acknowledged outbox not empty: %v, %v", entries, e)
	}
	entries, e = os.ReadDir(restarted.ReceiptsDir)
	if e != nil || len(entries) != 1 {
		t.Fatalf("restart replay not idempotent: %v, %v", entries, e)
	}
	persisted, e = os.ReadFile(filepath.Join(restarted.ReceiptsDir, receipt.ID+".json"))
	if e != nil || !bytes.Equal(persisted, body) {
		t.Fatalf("replay changed receipt: %v", e)
	}
	resp, e := server.Client().Get(o.ReceiptURL)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var receipts []Receipt
	if e = json.NewDecoder(resp.Body).Decode(&receipts); e != nil || len(receipts) != 1 || receipts[0].ID != receipt.ID {
		t.Fatalf("restart history: %+v, %v", receipts, e)
	}
}

func TestReceiptStorageFailureNeverAcknowledged(t *testing.T) {
	root := t.TempDir()
	s := NewFaultServer(root, FaultPlan{})
	s.ReceiptsDir = filepath.Join(root, "not-a-directory")
	if e := os.WriteFile(s.ReceiptsDir, []byte("blocked"), 0600); e != nil {
		t.Fatal(e)
	}
	server := httptest.NewServer(s)
	defer server.Close()
	o := DefaultAgentOptions()
	o.StateDir = filepath.Join(root, "edge")
	o.DeviceID = "test-edge"
	o.ReceiptURL = server.URL + "/receipts"
	o.AllowHTTP = true
	if e := queueReceipt(o, Summary{Release: "r1", Phase: "loaded", ArtifactSHA256: Hash([]byte("archive"))}); e != nil {
		t.Fatal(e)
	}
	if e := FlushReceipts(context.Background(), o); e == nil {
		t.Fatal("sender storage failure acknowledged")
	}
	entries, e := os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if e != nil || len(entries) != 1 {
		t.Fatalf("storage failure discarded outbox: %v, %v", entries, e)
	}
	// A corrupt receipt is also never acknowledged or added to sender history.
	s.ReceiptsDir = filepath.Join(root, "healthy-sender")
	body, e := os.ReadFile(filepath.Join(o.StateDir, "outbox", entries[0].Name()))
	if e != nil {
		t.Fatal(e)
	}
	var receipt Receipt
	if e = json.Unmarshal(body, &receipt); e != nil {
		t.Fatal(e)
	}
	receipt.Phase = "staged" // invalidates its content-derived ID
	body, e = json.Marshal(receipt)
	if e != nil {
		t.Fatal(e)
	}
	resp, e := server.Client().Post(o.ReceiptURL, "application/json", bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("tampered receipt accepted: %d", resp.StatusCode)
	}
	if _, e = os.Stat(s.ReceiptsDir); !os.IsNotExist(e) {
		t.Fatalf("tampered receipt persisted: %v", e)
	}
}

func TestReceiptDurableOutboxAndIdempotentSender(t *testing.T) {
	root := t.TempDir()
	s := NewFaultServer(root, FaultPlan{Offline: true})
	s.ReceiptsDir = filepath.Join(root, "sender-receipts")
	server := httptest.NewServer(s)
	defer server.Close()
	o := DefaultAgentOptions()
	o.StateDir = filepath.Join(root, "edge")
	o.DeviceID = "test-edge"
	o.ReceiptURL = server.URL + "/receipts"
	o.AllowHTTP = true
	summary := Summary{Release: "r1", Sequence: 1, Phase: "loaded", ArtifactSHA256: Hash([]byte("archive"))}
	if e := queueReceipt(o, summary); e != nil {
		t.Fatal(e)
	}
	entries, _ := os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if len(entries) != 1 {
		t.Fatal("receipt was not queued")
	}
	receiptFile := filepath.Join(o.StateDir, "outbox", entries[0].Name())
	body, _ := os.ReadFile(receiptFile)
	if e := FlushReceipts(context.Background(), o); e == nil {
		t.Fatal("expected offline error")
	}
	if _, e := os.Stat(receiptFile); e != nil {
		t.Fatal("offline attempt discarded receipt")
	}
	// Change the fault plan only between completed requests, not concurrently.
	s.Plan.Offline = false
	if e := FlushReceipts(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	entries, _ = os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if len(entries) != 0 {
		t.Fatal("acknowledged receipt remains")
	}
	if e := AtomicWrite(receiptFile, body, 0600); e != nil {
		t.Fatal(e)
	}
	if e := FlushReceipts(context.Background(), o); e != nil {
		t.Fatal(e)
	}
	saved, _ := os.ReadDir(s.ReceiptsDir)
	if len(saved) != 1 {
		t.Fatal("duplicate not idempotent")
	}
	resp, e := server.Client().Get(server.URL + "/receipts")
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	var receipts []Receipt
	if e = json.NewDecoder(resp.Body).Decode(&receipts); e != nil {
		t.Fatal(e)
	}
	if len(receipts) != 1 || receipts[0].Phase != "loaded" {
		t.Fatal("bad sender acknowledgement")
	}
}
