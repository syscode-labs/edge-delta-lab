package lab

import (
	"archive/tar"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"example.com/edge-delta-lab/hubclient"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type State struct {
	Sequence       uint64 `json:"accepted_sequence"`
	ManifestSHA256 string `json:"accepted_manifest_sha256"`
	ManifestURL    string `json:"manifest_url"`
	Release        string `json:"release"`
	Phase          string `json:"phase"`
	Artifact       string `json:"artifact"`
	Updated        string `json:"updated"`
	// The signed payload digest binds release, sequence, archive and expected
	// image IDs. Set only after successful load AND stored-config proof. This
	// records import completion, not current daemon inventory or running health.
	LoadedManifestSHA256 string `json:"loaded_manifest_sha256,omitempty"`
}
type Summary struct {
	Release            string  `json:"release"`
	Sequence           uint64  `json:"sequence"`
	Phase              string  `json:"phase"`
	Artifact           string  `json:"artifact"`
	ArtifactSHA256     string  `json:"artifact_sha256"`
	ArtifactBytes      int64   `json:"artifact_bytes"`
	UniqueChunks       int     `json:"unique_chunks"`
	ReusedChunks       int     `json:"reused_chunks"`
	ReusedRawBytes     int64   `json:"reused_raw_bytes"`
	DownloadedChunks   int64   `json:"downloaded_chunks"`
	DownloadedRawBytes int64   `json:"downloaded_raw_bytes"`
	ChunkBodyBytes     int64   `json:"chunk_response_body_bytes"`
	MetadataBodyBytes  int64   `json:"metadata_response_body_bytes"`
	UsefulEncodedBytes int64   `json:"useful_encoded_bytes"`
	RetryOverheadBytes int64   `json:"retry_overhead_body_bytes"`
	ChunkRequests      int64   `json:"chunk_requests"`
	MetadataRequests   int64   `json:"metadata_requests"`
	Retries            int64   `json:"retries"`
	IntegrityFailures  int64   `json:"integrity_failures"`
	Seconds            float64 `json:"elapsed_seconds"`
}
type AgentOptions struct {
	HubTLS                                    hubclient.Config
	ManifestURL, BaseURL, StateDir, PublicKey string
	ReceiptURL, DeviceID                      string
	Workers, MaxAttempts                      int
	Backoff, MaxBackoff, RequestTimeout       time.Duration
	MaxArtifact, ReserveBytes                 int64
	Offline, DockerLoad, AllowHTTP            bool
	Events                                    io.Writer
	Telemetry                                 *Telemetry
}

// Telemetry is a bounded, process-lifetime client snapshot. It contains no
// release, chunk, path, or error labels and is safe for concurrent updates.
type Telemetry struct {
	started                                                 time.Time
	state, active                                           atomic.Int64
	chunkBytes, metadataBytes                               atomic.Int64
	chunkRequests, metadataRequests                         atomic.Int64
	syncSuccess, syncFailure, retries, integrity, verified  atomic.Int64
	verifiedChunks, reusedChunks                            atomic.Int64
	lastDuration, lastSuccess                               atomic.Int64
	lastDownloadBytes, lastVerifiedChunks, lastReusedChunks atomic.Int64
}

const (
	clientUnknown int64 = iota
	clientIdle
	clientDownloading
	clientVerifying
	clientStaged
	clientLoading
	clientLoaded
	clientFailed
)

func NewTelemetry() *Telemetry {
	t := &Telemetry{started: time.Now()}
	t.state.Store(clientIdle)
	return t
}

func (t *Telemetry) Snapshot() map[string]any {
	if t == nil {
		return map[string]any{}
	}
	states := []string{"unknown", "idle", "downloading", "verifying", "staged", "loading", "loaded", "failed"}
	s := t.state.Load()
	if s < 0 || s >= int64(len(states)) {
		s = clientUnknown
	}
	last := t.lastSuccess.Load()
	lastSec := float64(0)
	if last > 0 {
		lastSec = float64(last) / 1e9
	}
	return map[string]any{
		"process_start_time_seconds": float64(t.started.UnixNano()) / 1e9,
		"chunk_response_body_bytes":  t.chunkBytes.Load(), "metadata_response_body_bytes": t.metadataBytes.Load(),
		"chunk_requests": t.chunkRequests.Load(), "metadata_requests": t.metadataRequests.Load(),
		"sync_success": t.syncSuccess.Load(), "sync_failure": t.syncFailure.Load(),
		"retries": t.retries.Load(), "integrity_failures": t.integrity.Load(),
		"verified_download_bytes": t.verified.Load(), "sync_active": t.active.Load(),
		"verified_chunks": t.verifiedChunks.Load(), "reused_chunks": t.reusedChunks.Load(),
		"state": states[s], "last_sync_duration_seconds": float64(t.lastDuration.Load()) / 1e9,
		"last_success_timestamp_seconds": lastSec,
		"last_sync_download_bytes":       t.lastDownloadBytes.Load(),
		"last_sync_verified_chunks":      t.lastVerifiedChunks.Load(),
		"last_sync_reused_chunks":        t.lastReusedChunks.Load(),
	}
}

type progress struct {
	wire, metadata, useful, requests, metaRequests, retries, integrity, committed, raw atomic.Int64
	logMu                                                                              sync.Mutex
	out                                                                                io.Writer
}

func (p *progress) event(kind string, fields map[string]any) {
	if p.out == nil {
		return
	}
	if fields == nil {
		fields = map[string]any{}
	}
	fields["event"] = kind
	fields["time"] = time.Now().UTC().Format(time.RFC3339Nano)
	p.logMu.Lock()
	defer p.logMu.Unlock()
	_ = json.NewEncoder(p.out).Encode(fields)
}
func DefaultAgentOptions() AgentOptions {
	return AgentOptions{Workers: 2, MaxAttempts: 12, Backoff: 200 * time.Millisecond, MaxBackoff: 5 * time.Second, RequestTimeout: 30 * time.Second, MaxArtifact: 10 << 30, ReserveBytes: 64 << 20}
}
func readState(dir string) (State, error) {
	var s State
	b, e := os.ReadFile(filepath.Join(dir, "state.json"))
	if os.IsNotExist(e) {
		return s, nil
	}
	if e != nil {
		return s, e
	}
	e = json.Unmarshal(b, &s)
	return s, e
}
func writeState(dir string, s State) error {
	s.Updated = time.Now().UTC().Format(time.RFC3339Nano)
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	return AtomicWrite(filepath.Join(dir, "state.json"), b, 0600)
}

// verifyLoadedConfigIdentity proves the signed config digest is really stored.
// Docker 29 containerd-backed stores label images with the OCI manifest digest,
// so inspect.Id never equals the config digest on either the build or the load
// side. The daemon's own load report names the reference it just stored; that
// reference is saved back out of the store and the preserved config bytes must
// hash to the signed image ID. Unparseable load output, refused saves, config
// bytes that hash differently and unaccounted signed IDs all fail closed.
func verifyLoadedConfigIdentity(ctx context.Context, loadReport string, imageIDs []string) error {
	refs := loadReportRefs(loadReport)
	if len(refs) == 0 {
		return errors.New("docker load report names no loaded image; cannot prove stored identity")
	}
	signed := map[string]bool{}
	for _, id := range imageIDs {
		signed[strings.TrimSpace(id)] = true
	}
	covered := map[string]bool{}
	for _, ref := range refs {
		dir, e := os.MkdirTemp("", "edgelab-loadcheck-")
		if e != nil {
			return e
		}
		path := filepath.Join(dir, "roundtrip.tar")
		save := exec.CommandContext(ctx, "docker", "image", "save", "--output", path, ref)
		save.Stdout, save.Stderr = io.Discard, io.Discard
		e = save.Run()
		if e == nil {
			var configID string
			configID, e = savedConfigID(path)
			if e == nil {
				if !signed[configID] {
					e = fmt.Errorf("stored config %s is not a signed image ID", configID)
				}
				covered[configID] = true
			}
		}
		os.RemoveAll(dir)
		if e != nil {
			return fmt.Errorf("post-load proof of %s failed: %w", ref, e)
		}
	}
	for id := range signed {
		if !covered[id] {
			return fmt.Errorf("load report and store never accounted for signed image ID %s", id)
		}
	}
	return nil
}

// loadReportRefs extracts the image references the daemon reported loading.
func loadReportRefs(report string) []string {
	var refs []string
	for _, m := range regexp.MustCompile(`Loaded image: (\S+)`).FindAllStringSubmatch(report, -1) {
		refs = append(refs, m[1])
	}
	for _, m := range regexp.MustCompile(`Loaded image ID: (\S+)`).FindAllStringSubmatch(report, -1) {
		if !hexDigest.MatchString(strings.TrimPrefix(m[1], "sha256:")) {
			continue
		}
		// An ID reported by the daemon itself is direct store evidence.
		refs = append(refs, m[1])
	}
	return refs
}

// savedConfigID hashes the preserved config bytes of the single image in a
// saved archive. The blob filename is not trusted; the bytes are hashed.
func savedConfigID(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	tr := tar.NewReader(f)
	member := func(name string, limit int64) ([]byte, error) {
		if _, e := f.Seek(0, io.SeekStart); e != nil {
			return nil, e
		}
		tr = tar.NewReader(f)
		for {
			h, e := tr.Next()
			if e == io.EOF {
				return nil, fmt.Errorf("saved archive lacks member %s", name)
			}
			if e != nil {
				return nil, e
			}
			if h.Name != name || !h.FileInfo().Mode().IsRegular() || h.Size > limit {
				continue
			}
			return io.ReadAll(io.LimitReader(tr, limit))
		}
	}
	b, e := member("manifest.json", 4<<20)
	if e != nil {
		return "", e
	}
	var list []struct {
		Config string `json:"Config"`
	}
	if e = json.Unmarshal(b, &list); e != nil || len(list) != 1 {
		return "", fmt.Errorf("saved archive must describe exactly one image: err=%v images=%d", e, len(list))
	}
	name := list[0].Config[strings.LastIndexByte(list[0].Config, '/')+1:]
	if !hexDigest.MatchString(name) {
		return "", fmt.Errorf("saved config path is not a content digest: %s", list[0].Config)
	}
	b, e = member(list[0].Config, 4<<20)
	if e != nil {
		return "", e
	}
	return "sha256:" + Hash(b), nil
}
func allowedURL(raw string, allowHTTP bool) error {
	u, e := url.Parse(raw)
	if e != nil {
		return e
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("URLs require a host and must not include credentials, query or fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowHTTP {
		return nil
	}
	return errors.New("HTTPS required; --allow-http is for an isolated lab only")
}
func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type permanentError struct{ error }

var errNotModified = errors.New("not modified")

func request(ctx context.Context, client *http.Client, uri string, limit int64, etag string, device string) ([]byte, int64, time.Duration, error) {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if e != nil {
		return nil, 0, 0, permanentError{e}
	}
	req.Header.Set("Accept-Encoding", "identity")
	// Identify this device to the origin so hub-side per-device accounting
	// (X-Edgelab-Device) covers agent traffic too, not just push dialers.
	if device != "" {
		req.Header.Set("X-Edgelab-Device", device)
	}
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	r, e := client.Do(req)
	if e != nil {
		return nil, 0, 0, e
	}
	defer r.Body.Close()
	if r.StatusCode == http.StatusNotModified && etag != "" {
		return nil, 0, 0, errNotModified
	}
	var after time.Duration
	if sec, e := strconv.Atoi(r.Header.Get("Retry-After")); e == nil && sec > 0 {
		after = time.Duration(sec) * time.Second
	}
	if r.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
		err := fmt.Errorf("HTTP %d from %s", r.StatusCode, uri)
		if r.StatusCode == 429 || r.StatusCode >= 500 {
			return nil, int64(len(b)), after, err
		}
		return nil, int64(len(b)), 0, permanentError{err}
	}
	if r.ContentLength > limit {
		return nil, 0, 0, permanentError{errors.New("response exceeds signed size limit")}
	}
	b, e := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if int64(len(b)) > limit {
		return nil, int64(len(b)), 0, permanentError{errors.New("response exceeds signed size limit")}
	}
	return b, int64(len(b)), 0, e
}
func retry(ctx context.Context, o AgentOptions, p *progress, label string, fn func() (time.Duration, error)) error {
	rng := rand.New(rand.NewSource(int64(len(label)) + int64(time.Now().Nanosecond())))
	delay := o.Backoff
	for attempt := 1; ; attempt++ {
		if e := ctx.Err(); e != nil {
			return e
		}
		after, e := fn()
		if e == nil {
			return nil
		}
		var permanent permanentError
		if errors.As(e, &permanent) {
			return e
		}
		if o.MaxAttempts > 0 && attempt >= o.MaxAttempts {
			return fmt.Errorf("%s exhausted %d attempts: %w", label, attempt, e)
		}
		d := time.Duration(float64(delay) * (0.75 + rng.Float64()*0.5))
		if d > o.MaxBackoff {
			d = o.MaxBackoff
		}
		if after > d {
			d = after
		}
		p.retries.Add(1)
		if o.Telemetry != nil {
			o.Telemetry.retries.Add(1)
		}
		p.event("retry", map[string]any{"object": label, "attempt": attempt, "delay_ms": d.Milliseconds(), "error": e.Error()})
		if e = sleepContext(ctx, d); e != nil {
			return e
		}
		if delay < o.MaxBackoff/2 {
			delay *= 2
		} else {
			delay = o.MaxBackoff
		}
	}
}
func Sync(ctx context.Context, o AgentOptions) (summary Summary, err error) {
	if err := ctx.Err(); err != nil {
		return summary, err
	}
	tlsConfig, err := o.HubTLS.TLSConfig()
	if err != nil {
		return summary, err
	}
	for _, raw := range []string{o.ManifestURL, o.BaseURL, o.ReceiptURL} {
		if raw != "" {
			if err := o.HubTLS.ValidateURL(raw); err != nil {
				return summary, err
			}
		}
	}
	start := time.Now()
	p := &progress{out: o.Events}
	if o.Telemetry != nil {
		o.Telemetry.active.Store(1)
		o.Telemetry.state.Store(clientDownloading)
	}
	defer func() {
		summary.Seconds = time.Since(start).Seconds()
		summary.ChunkBodyBytes = p.wire.Load()
		summary.MetadataBodyBytes = p.metadata.Load()
		summary.UsefulEncodedBytes = p.useful.Load()
		summary.RetryOverheadBytes = summary.ChunkBodyBytes - summary.UsefulEncodedBytes
		summary.ChunkRequests = p.requests.Load()
		summary.MetadataRequests = p.metaRequests.Load()
		summary.Retries = p.retries.Load()
		summary.IntegrityFailures = p.integrity.Load()
		summary.DownloadedChunks = p.committed.Load()
		summary.DownloadedRawBytes = p.raw.Load()
		if o.Telemetry != nil {
			o.Telemetry.active.Store(0)
			o.Telemetry.lastDuration.Store(time.Since(start).Nanoseconds())
			o.Telemetry.lastDownloadBytes.Store(summary.UsefulEncodedBytes)
			o.Telemetry.lastVerifiedChunks.Store(summary.DownloadedChunks)
			o.Telemetry.lastReusedChunks.Store(int64(summary.ReusedChunks))
			if err != nil {
				o.Telemetry.syncFailure.Add(1)
				o.Telemetry.state.Store(clientFailed)
			} else {
				o.Telemetry.syncSuccess.Add(1)
				o.Telemetry.verifiedChunks.Add(summary.DownloadedChunks)
				o.Telemetry.reusedChunks.Add(int64(summary.ReusedChunks))
				o.Telemetry.lastSuccess.Store(time.Now().UnixNano())
				o.Telemetry.state.Store(clientLoaded)
			}
		}
		if err != nil {
			p.event("failed", map[string]any{"error": err.Error()})
		} else if o.ReceiptURL != "" && !o.Offline {
			if e := queueReceipt(o, summary); e != nil {
				p.event("receipt_queue_failed", map[string]any{"error": e.Error()})
			} else if e := FlushReceipts(ctx, o); e != nil {
				p.event("receipt_pending", map[string]any{"error": e.Error()})
			}
		}
	}()
	if o.ReceiptURL != "" {
		if !safeRelease.MatchString(o.DeviceID) {
			return summary, errors.New("receipt reporting requires a valid --device-id")
		}
		if e := allowedURL(o.ReceiptURL, o.AllowHTTP); e != nil {
			return summary, e
		}
		if e := FlushReceipts(ctx, o); e != nil {
			p.event("receipt_pending", map[string]any{"error": e.Error()})
		}
	}
	if o.Workers < 1 || o.Workers > 32 || o.Backoff <= 0 || o.MaxBackoff < o.Backoff || o.RequestTimeout <= 0 || o.MaxArtifact <= 0 || o.ReserveBytes < 0 {
		return summary, errors.New("invalid agent options")
	}
	lock, e := LockState(o.StateDir)
	if e != nil {
		return summary, e
	}
	defer UnlockState(lock)
	if e = CleanTemps(o.StateDir); e != nil {
		return summary, e
	}
	pub, e := ReadKey(o.PublicKey, ed25519.PublicKeySize)
	if e != nil {
		return summary, e
	}
	old, e := readState(o.StateDir)
	if e != nil {
		return summary, e
	}
	transport := &http.Transport{DisableCompression: true, MaxIdleConnsPerHost: o.Workers + 1, ResponseHeaderTimeout: o.RequestTimeout, DialContext: (&net.Dialer{Timeout: o.RequestTimeout, KeepAlive: 30 * time.Second}).DialContext}
	transport.TLSClientConfig = tlsConfig
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: o.RequestTimeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return errors.New("redirects are disabled") }}
	var envelope []byte
	if o.Offline {
		if old.ManifestSHA256 == "" {
			return summary, errors.New("no accepted manifest available offline")
		}
		if o.ManifestURL != "" && o.ManifestURL != old.ManifestURL {
			return summary, errors.New("offline mode only accepts the current cached manifest")
		}
		envelope, e = os.ReadFile(filepath.Join(o.StateDir, "manifests", old.ManifestSHA256+".json"))
		if e != nil {
			return summary, e
		}
		o.ManifestURL = old.ManifestURL
	} else {
		if e = allowedURL(o.ManifestURL, o.AllowHTTP); e != nil {
			return summary, e
		}
		if e = allowedURL(o.BaseURL, o.AllowHTTP); e != nil {
			return summary, e
		}
		var cached []byte
		var etag string
		if old.ManifestURL == o.ManifestURL && old.ManifestSHA256 != "" {
			b, err := os.ReadFile(filepath.Join(o.StateDir, "manifests", old.ManifestSHA256+".json"))
			if err == nil {
				_, cachedDigest, err := Verify(b, pub, o.MaxArtifact)
				if err == nil && cachedDigest == old.ManifestSHA256 {
					cached = b
					etag = "\"" + Hash(b) + "\""
				}
			}
		}
		e = retry(ctx, o, p, "manifest", func() (time.Duration, error) {
			p.metaRequests.Add(1)
			b, n, after, err := request(ctx, client, o.ManifestURL, MaxManifestBytes, etag, o.DeviceID)
			p.metadata.Add(n)
			if o.Telemetry != nil {
				o.Telemetry.metadataBytes.Add(n)
				o.Telemetry.metadataRequests.Add(1)
			}
			if errors.Is(err, errNotModified) && cached != nil {
				envelope = cached
				return 0, nil
			}
			if err == nil {
				envelope = b
			}
			return after, err
		})
		if e != nil {
			return summary, e
		}
	}
	m, digest, e := Verify(envelope, pub, o.MaxArtifact)
	if e != nil {
		return summary, e
	}
	if m.Sequence < old.Sequence {
		return summary, fmt.Errorf("rollback rejected: sequence %d < %d", m.Sequence, old.Sequence)
	}
	if m.Sequence == old.Sequence && old.ManifestSHA256 != "" && digest != old.ManifestSHA256 {
		return summary, errors.New("same-sequence equivocation rejected")
	}
	// Persist an anti-rollback high-water mark before fetching content.
	if e = AtomicWrite(filepath.Join(o.StateDir, "manifests", digest+".json"), envelope, 0600); e != nil {
		return summary, e
	}
	output := filepath.Join(o.StateDir, "staged", m.ArtifactSHA256+".tar")
	s := State{Sequence: m.Sequence, ManifestSHA256: digest, ManifestURL: o.ManifestURL, Release: m.Release, Phase: "planning", Artifact: output, LoadedManifestSHA256: old.LoadedManifestSHA256}
	if e = writeState(o.StateDir, s); e != nil {
		return summary, e
	}
	summary.Release = m.Release
	summary.Sequence = m.Sequence
	summary.Artifact = output
	summary.ArtifactSHA256 = m.ArtifactSHA256
	summary.ArtifactBytes = m.ArtifactSize
	unique := Unique(m)
	summary.UniqueChunks = len(unique)
	missing := []Chunk{}
	var missingBytes int64
	for _, c := range unique {
		if e = ctx.Err(); e != nil {
			return summary, e
		}
		if ValidChunk(o.StateDir, c) {
			summary.ReusedChunks++
			summary.ReusedRawBytes += c.Size
		} else {
			missing = append(missing, c)
			missingBytes += c.Size
		}
	}
	p.event("plan", map[string]any{"release": m.Release, "unique_chunks": len(unique), "reused_chunks": summary.ReusedChunks, "missing_chunks": len(missing), "missing_raw_bytes": missingBytes})
	if o.Offline && len(missing) > 0 {
		return summary, fmt.Errorf("offline: %d chunks missing or corrupt", len(missing))
	}
	h, n, _ := FileHash(output)
	archiveValid := h == m.ArtifactSHA256 && n == m.ArtifactSize
	needed := missingBytes + o.ReserveBytes
	if !archiveValid {
		needed += m.ArtifactSize
	}
	free, e := AvailableBytes(o.StateDir)
	if e != nil {
		return summary, e
	}
	if free < needed {
		return summary, fmt.Errorf("insufficient space: need %d bytes, have %d (no pruning performed)", needed, free)
	}
	s.Phase = "fetching"
	if e = writeState(o.StateDir, s); e != nil {
		return summary, e
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan Chunk)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	for i := 0; i < o.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for c := range jobs {
				e := retry(fetchCtx, o, p, c.SHA256, func() (time.Duration, error) {
					p.requests.Add(1)
					b, n, after, e := request(fetchCtx, client, strings.TrimRight(o.BaseURL, "/")+"/"+BlobRel(c), c.EncodedSize, "", o.DeviceID)
					p.wire.Add(n)
					if o.Telemetry != nil {
						o.Telemetry.chunkBytes.Add(n)
						o.Telemetry.chunkRequests.Add(1)
					}
					if e != nil {
						return after, e
					}
					raw, e := Decode(b, c)
					if e != nil {
						p.integrity.Add(1)
						if o.Telemetry != nil {
							o.Telemetry.integrity.Add(1)
							o.Telemetry.state.Store(clientVerifying)
						}
						p.event("integrity_failure", map[string]any{"chunk": c.SHA256})
						return 0, e
					}
					// Disk failures are not transport failures: stop rather than redownload.
					if e = AtomicWrite(CachePath(o.StateDir, c), raw, 0600); e != nil {
						return 0, permanentError{e}
					}
					p.committed.Add(1)
					p.raw.Add(c.Size)
					p.useful.Add(c.EncodedSize)
					if o.Telemetry != nil {
						o.Telemetry.verified.Add(c.EncodedSize)
					}
					p.event("chunk_committed", map[string]any{"chunk": c.SHA256, "raw_bytes": c.Size, "encoded_bytes": c.EncodedSize})
					return 0, nil
				})
				if e != nil {
					errOnce.Do(func() { firstErr = e; cancel() })
					return
				}
			}
		}()
	}
send:
	for _, c := range missing {
		select {
		case jobs <- c:
		case <-fetchCtx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr != nil {
		return summary, firstErr
	}
	if e = ctx.Err(); e != nil {
		return summary, e
	}
	s.Phase = "assembling"
	if e = writeState(o.StateDir, s); e != nil {
		return summary, e
	}
	if !archiveValid {
		if e = assemble(ctx, o.StateDir, m, output); e != nil {
			return summary, e
		}
	}
	// No Docker call has occurred before signature, chunks, size and full archive verification.
	if o.DockerLoad {
		if m.Kind != "docker-archive" || len(m.ImageIDs) == 0 {
			return summary, errors.New("docker-load requires docker-archive and signed expected image IDs")
		}
		if s.LoadedManifestSHA256 != digest {
			// Clear before invoking Docker: a failed/interrupted load or failed proof
			// is ambiguous and must be retried, never inferred from the phase alone.
			s.LoadedManifestSHA256 = ""
			s.Phase = "loading"
			if e = writeState(o.StateDir, s); e != nil {
				return summary, e
			}
			cmd := exec.CommandContext(ctx, "docker", "image", "load", "--input", output)
			loadReport, loadErr := func() (string, error) {
				var buf strings.Builder
				cmd.Stdout, cmd.Stderr = &buf, &buf
				e := cmd.Run()
				return buf.String(), e
			}()
			if loadErr != nil {
				return summary, fmt.Errorf("local docker load failed; archive retained: %w", loadErr)
			}
			if e = verifyLoadedConfigIdentity(ctx, loadReport, m.ImageIDs); e != nil {
				return summary, e
			}
			if o.Events != nil {
				fmt.Fprintln(o.Events, strings.TrimRight(loadReport, "\n"))
			}
			s.LoadedManifestSHA256 = digest
		}
		s.Phase = "loaded"
	} else {
		s.Phase = "staged"
	}
	if e = writeState(o.StateDir, s); e != nil {
		return summary, e
	}
	summary.Phase = s.Phase
	p.event("release_ready", map[string]any{"release": m.Release, "phase": s.Phase, "artifact_sha256": m.ArtifactSHA256})
	return summary, nil
}
func assemble(ctx context.Context, state string, m Manifest, out string) error {
	dir := filepath.Dir(out)
	if e := EnsureDir(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".tmp-archive-")
	if e != nil {
		return e
	}
	defer f.Close()
	defer os.Remove(f.Name())
	h := sha256.New()
	writer := io.MultiWriter(f, h)
	var total int64
	for _, c := range m.Chunks {
		if e = ctx.Err(); e != nil {
			return e
		}
		src, e := os.Open(CachePath(state, c))
		if e != nil {
			return e
		}
		n, e := io.Copy(writer, io.LimitReader(src, c.Size+1))
		src.Close()
		if e != nil {
			return e
		}
		if n != c.Size {
			return errors.New("cached chunk size changed during assembly")
		}
		total += n
	}
	if total != m.ArtifactSize || hex.EncodeToString(h.Sum(nil)) != m.ArtifactSHA256 {
		return errors.New("assembled artifact integrity failure")
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(f.Name(), out); e != nil {
		return e
	}
	return SyncDir(dir)
}

// WriteSummary commits human/machine-readable output and a node_exporter textfile.
// Counters are per successful invocation, therefore exported as gauges.
func WriteSummary(state string, s Summary) error {
	b, e := json.MarshalIndent(s, "", "  ")
	if e != nil {
		return e
	}
	if e = AtomicWrite(filepath.Join(state, "summary.json"), b, 0600); e != nil {
		return e
	}
	metrics := fmt.Sprintf("# TYPE edge_update_last_chunk_body_bytes gauge\nedge_update_last_chunk_body_bytes %d\n# TYPE edge_update_last_retry_overhead_bytes gauge\nedge_update_last_retry_overhead_bytes %d\n# TYPE edge_update_last_reused_chunks gauge\nedge_update_last_reused_chunks %d\n# TYPE edge_update_last_elapsed_seconds gauge\nedge_update_last_elapsed_seconds %.6f\n# TYPE edge_update_last_success_timestamp_seconds gauge\nedge_update_last_success_timestamp_seconds %d\n# TYPE edge_update_accepted_sequence gauge\nedge_update_accepted_sequence %d\n", s.ChunkBodyBytes, s.RetryOverheadBytes, s.ReusedChunks, s.Seconds, time.Now().Unix(), s.Sequence)
	return AtomicWrite(filepath.Join(state, "agent.prom"), []byte(metrics), 0644)
}
