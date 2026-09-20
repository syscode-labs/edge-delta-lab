// Package receiverloop owns the shared CLI/embedded watch lifecycle.
package receiverloop

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/push"
	"strings"
	"time"
)

func Run(ctx context.Context, o lab.AgentOptions, poll time.Duration, eventsURL string, onError func(error), onSync func(lab.Summary)) error {
	if poll <= 0 {
		return errors.New("poll must be positive")
	}
	if _, err := o.HubTLS.TLSConfig(); err != nil {
		return err
	}
	for _, raw := range []string{o.ManifestURL, o.BaseURL, o.ReceiptURL, eventsURL} {
		if raw != "" {
			if err := o.HubTLS.ValidateURL(raw); err != nil {
				return err
			}
		}
	}
	if eventsURL != "" && !strings.HasPrefix(eventsURL, "wss://") && !(o.AllowHTTP && strings.HasPrefix(eventsURL, "ws://")) {
		return errors.New("events URL requires WSS (WS requires allow-http)")
	}
	if o.Events != nil {
		o.Events = &lockedWriter{writer: o.Events}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	kick := make(chan struct{}, 1)
	if eventsURL != "" && !o.Offline {
		if err := o.HubTLS.ValidateURL(eventsURL); err != nil {
			return err
		}
		pub, err := lab.ReadKey(o.PublicKey, ed25519.PublicKeySize)
		if err != nil {
			return err
		}
		tlsConfig, err := o.HubTLS.TLSConfig()
		if err != nil {
			return err
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			push.Run(ctx, push.ClientOptions{URL: eventsURL, Device: o.DeviceID, TLSConfig: tlsConfig,
				OnAnnounce: func(h push.Announce) {
					m, err := push.Accept(h, ed25519.PublicKey(pub), o.MaxArtifact)
					if o.Events != nil {
						if err != nil {
							_ = json.NewEncoder(o.Events).Encode(map[string]any{"event": "announce_rejected", "error": err.Error()})
						} else {
							_ = json.NewEncoder(o.Events).Encode(map[string]any{"event": "announce_accepted", "release": m.Release, "sequence": m.Sequence})
						}
					}
					if err == nil {
						select {
						case kick <- struct{}{}:
						default:
						}
					}
				}})
		}()
		defer func() { cancel(); <-done }()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		s, err := lab.Sync(ctx, o)
		if err == nil {
			err = lab.WriteSummary(o.StateDir, s)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if onError != nil {
				onError(err)
			}
		} else if onSync != nil {
			onSync(s)
		}
		timer := time.NewTimer(poll)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		case <-kick:
			timer.Stop()
		}
	}
}
