package lab

// Sender acknowledgements are an outbound control channel. They do not prove
// that bytes merely written by the origin have been committed by the device.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"example.com/edge-delta-lab/hubclient"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Receipt struct {
	ID             string  `json:"id"`
	DeviceID       string  `json:"device_id"`
	Release        string  `json:"release"`
	Phase          string  `json:"phase"`
	ArtifactSHA256 string  `json:"artifact_sha256"`
	RecordedAt     string  `json:"recorded_at"`
	Summary        Summary `json:"summary"`
}

func (r Receipt) digest() string { r.ID = ""; b, _ := json.Marshal(r); return Hash(b) }
func (r Receipt) validate() error {
	if !safeRelease.MatchString(r.DeviceID) || !safeRelease.MatchString(r.Release) || !hexDigest.MatchString(r.ArtifactSHA256) {
		return errors.New("invalid receipt identity")
	}
	if r.Phase != "staged" && r.Phase != "loaded" {
		return errors.New("receipt must acknowledge staged or loaded, not running")
	}
	if r.ID != r.digest() {
		return errors.New("receipt ID mismatch")
	}
	if _, e := time.Parse(time.RFC3339Nano, r.RecordedAt); e != nil {
		return e
	}
	return nil
}

// Validate is the exported form of validate, for observers outside package
// lab (e.g. the admin socket) that must apply the same receipt rules without
// being able to alter them.
func (r Receipt) Validate() error { return r.validate() }

func queueReceipt(o AgentOptions, s Summary) error {
	r := Receipt{DeviceID: o.DeviceID, Release: s.Release, Phase: s.Phase, ArtifactSHA256: s.ArtifactSHA256, RecordedAt: time.Now().UTC().Format(time.RFC3339Nano), Summary: s}
	r.ID = r.digest()
	if e := r.validate(); e != nil {
		return e
	}
	b, e := json.Marshal(r)
	if e != nil {
		return e
	}
	return AtomicWrite(filepath.Join(o.StateDir, "outbox", r.ID+".json"), b, 0600)
}

// FlushReceipts retries durable acknowledgements, independently of image bytes.
// A failed POST leaves the file intact. An accepted POST followed by a crash is
// safely repeated: the origin keys receipts by their content-derived ID.
func FlushReceipts(ctx context.Context, o AgentOptions) error {
	if o.ReceiptURL == "" || o.Offline {
		return nil
	}
	if e := allowedURL(o.ReceiptURL, o.AllowHTTP); e != nil {
		return e
	}
	entries, e := os.ReadDir(filepath.Join(o.StateDir, "outbox"))
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	client, e := hubclient.New(o.HubTLS, 3*time.Second)
	if e != nil {
		return e
	}
	defer client.CloseIdleConnections()
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(o.StateDir, "outbox", entry.Name())
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, o.ReceiptURL, bytes.NewReader(b))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return fmt.Errorf("receipt HTTP %d", resp.StatusCode)
		}
		if e = os.Remove(path); e != nil && !os.IsNotExist(e) {
			return e
		}
		if e = SyncDir(filepath.Dir(path)); e != nil {
			return e
		}
	}
	return nil
}

func (s *FaultServer) receipts(w http.ResponseWriter, r *http.Request) {
	if s.ReceiptsDir == "" {
		http.Error(w, "receipts disabled; configure --receipts-dir", http.StatusNotFound)
		return
	}
	switch r.Method {
	case http.MethodGet:
		entries, e := os.ReadDir(s.ReceiptsDir)
		if os.IsNotExist(e) {
			entries = nil
		} else if e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		out := []Receipt{}
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			b, e := os.ReadFile(filepath.Join(s.ReceiptsDir, entry.Name()))
			if e != nil {
				http.Error(w, e.Error(), 500)
				return
			}
			var receipt Receipt
			if json.Unmarshal(b, &receipt) != nil || receipt.validate() != nil {
				http.Error(w, "corrupt persisted receipt", 500)
				return
			}
			out = append(out, receipt)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(out)
	case http.MethodPost:
		// The fault plan also applies to outbound acknowledgement attempts.
		plan, e := s.plan()
		if e != nil {
			http.Error(w, "invalid plan", 500)
			return
		}
		if plan.Offline || (plan.OfflineForMS > 0 && time.Since(s.Started) < time.Duration(plan.OfflineForMS)*time.Millisecond) {
			w.WriteHeader(503)
			return
		}
		var receipt Receipt
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		decoder.DisallowUnknownFields()
		if e = decoder.Decode(&receipt); e != nil {
			http.Error(w, "invalid receipt JSON", 400)
			return
		}
		if e = receipt.validate(); e != nil {
			http.Error(w, e.Error(), 400)
			return
		}
		b, _ := json.Marshal(receipt)
		// A duplicate has exactly the same canonical body. Replacing it atomically
		// is idempotent, including after an origin restart.
		if e = AtomicWrite(filepath.Join(s.ReceiptsDir, receipt.ID+".json"), b, 0600); e != nil {
			http.Error(w, e.Error(), 500)
			return
		}
		w.WriteHeader(http.StatusCreated)
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
	}
}
