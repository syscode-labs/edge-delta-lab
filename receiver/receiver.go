// Package receiver embeds the signed-artifact reconciliation agent. It does not
// start a listener or a hub, and Docker import is always an explicit opt-in.
package receiver

import (
	"context"
	"crypto/ed25519"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"example.com/edge-delta-lab/hubclient"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/receiverloop"
)

// Config is copied by New. Use DefaultConfig for retry and resource defaults.
// StateDir must have one owner across processes; never share it between devices.
// Events receives NDJSON from the transfer engine. OnError receives failed
// reconciliations before retry. Hooks are synchronous and must not block.
type Config struct {
	ManifestURL, BaseURL, StateDir, PublicKey string
	ReceiptURL, DeviceID, EventsURL           string
	HubTLS                                    hubclient.Config
	Poll                                      time.Duration
	Workers, MaxAttempts                      int
	Backoff, MaxBackoff, RequestTimeout       time.Duration
	MaxArtifact, ReserveBytes                 int64
	Offline, DockerLoad, AllowHTTP            bool
	Events                                    io.Writer
	OnError                                   func(error)
	OnSync                                    func(Result)
}

func DefaultConfig() Config {
	o := lab.DefaultAgentOptions()
	return Config{Poll: 30 * time.Second, Workers: o.Workers, MaxAttempts: o.MaxAttempts,
		Backoff: o.Backoff, MaxBackoff: o.MaxBackoff, RequestTimeout: o.RequestTimeout,
		MaxArtifact: o.MaxArtifact, ReserveBytes: o.ReserveBytes}
}

// Result reports a completed, verified reconciliation. Loaded is not running or healthy.
type Result struct {
	Release, Phase, Artifact string
	Sequence                 uint64
}

// Agent owns its run lifecycle, not the caller's context or event writer.
// Run may be invoked again after it returns, but never concurrently.
type Agent struct {
	config  Config
	running atomic.Bool
}

// New validates configuration without network access or creating state files.
func New(c Config) (*Agent, error) {
	if c.Poll <= 0 || c.StateDir == "" || c.PublicKey == "" || c.Workers < 1 || c.Workers > 32 || c.Backoff <= 0 || c.MaxBackoff < c.Backoff || c.RequestTimeout <= 0 || c.MaxArtifact <= 0 || c.ReserveBytes < 0 {
		return nil, errors.New("invalid receiver configuration (start with DefaultConfig)")
	}
	if !c.Offline && (c.ManifestURL == "" || c.BaseURL == "") {
		return nil, errors.New("receiver requires manifest and base URLs")
	}
	for _, raw := range []string{c.ManifestURL, c.BaseURL, c.ReceiptURL, c.EventsURL} {
		if raw == "" {
			continue
		}
		if err := c.HubTLS.ValidateURL(raw); err != nil {
			return nil, err
		}
		// Match the transfer engine's plaintext opt-in for websocket hints too.
		if !c.AllowHTTP && (len(raw) >= 5 && raw[:5] == "http:" || len(raw) >= 3 && raw[:3] == "ws:") {
			return nil, errors.New("plaintext requires AllowHTTP")
		}
	}
	if _, err := c.HubTLS.TLSConfig(); err != nil {
		return nil, err
	}
	if _, err := lab.ReadKey(c.PublicKey, ed25519.PublicKeySize); err != nil {
		return nil, err
	}
	return &Agent{config: c}, nil
}

// Run blocks until cancellation, returning ctx.Err(). Reconciliation failures
// are reported to OnError and retried at Poll; startup failures return directly.
// It joins the optional websocket worker before returning. In-flight local
// verification/file commits finish safely; cancellation never discards state.
func (a *Agent) Run(ctx context.Context) error {
	if !a.running.CompareAndSwap(false, true) {
		return errors.New("receiver already running")
	}
	defer a.running.Store(false)
	if err := ctx.Err(); err != nil {
		return err
	}
	c := a.config
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	o := lab.AgentOptions{ManifestURL: c.ManifestURL, BaseURL: c.BaseURL, StateDir: c.StateDir, PublicKey: c.PublicKey,
		ReceiptURL: c.ReceiptURL, DeviceID: c.DeviceID, HubTLS: c.HubTLS, Workers: c.Workers, MaxAttempts: c.MaxAttempts,
		Backoff: c.Backoff, MaxBackoff: c.MaxBackoff, RequestTimeout: c.RequestTimeout, MaxArtifact: c.MaxArtifact,
		ReserveBytes: c.ReserveBytes, Offline: c.Offline, DockerLoad: c.DockerLoad, AllowHTTP: c.AllowHTTP, Events: c.Events}
	return receiverloop.Run(ctx, o, c.Poll, c.EventsURL, c.OnError, func(s lab.Summary) {
		if c.OnSync != nil {
			c.OnSync(Result{Release: s.Release, Phase: s.Phase, Artifact: s.Artifact, Sequence: s.Sequence})
		}
	})
}
