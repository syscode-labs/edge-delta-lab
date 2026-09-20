package push_test

import (
	"context"
	"example.com/edge-delta-lab/hubclient"
	"example.com/edge-delta-lab/internal/push"
	"example.com/edge-delta-lab/internal/testtls"
	"io"
	"log"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMutualTLSWebsocketAndIdleCancellation(t *testing.T) {
	m := testtls.New(t)
	b := push.NewBroker()
	srv := httptest.NewUnstartedServer(b.Handler())
	srv.TLS = m.Server
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	defer srv.Close()
	u := strings.Replace(srv.URL, "https:", "wss:", 1)
	for _, tc := range []struct {
		name    string
		config  hubclient.Config
		allowed bool
	}{{"denied", hubclient.Config{CA: m.Client.CA}, false}, {"allowed", m.Client, true}} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := tc.config.TLSConfig()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				push.Run(ctx, push.ClientOptions{URL: u, TLSConfig: cfg, Backoff: 10 * time.Millisecond})
			}()
			deadline := time.NewTimer(200 * time.Millisecond)
			defer deadline.Stop()
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			connected := false
		wait:
			for {
				select {
				case <-deadline.C:
					break wait
				case <-tick.C:
					if b.Count() > 0 {
						connected = true
						break wait
					}
				}
			}
			if connected != tc.allowed {
				t.Errorf("connected=%v want %v", connected, tc.allowed)
			}
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("idle websocket blocked shutdown")
			}
		})
	}
}
