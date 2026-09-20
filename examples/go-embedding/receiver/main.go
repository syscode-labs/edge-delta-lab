// Receiver embeds reconciliation without a hub, listener, or Docker side effect.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"example.com/edge-delta-lab/receiver"
)

func main() {
	c := receiver.DefaultConfig()
	flag.StringVar(&c.BaseURL, "hub", "https://hub.example.test", "hub base URL")
	flag.StringVar(&c.ManifestURL, "manifest", "https://hub.example.test/releases/desired.json", "signed channel URL")
	flag.StringVar(&c.PublicKey, "pub", "work/keys/publisher.pub", "pinned signing public key")
	flag.StringVar(&c.StateDir, "state", "work/receiver", "exclusive persistent state")
	flag.StringVar(&c.EventsURL, "events-url", "", "optional WSS events URL")
	flag.StringVar(&c.HubTLS.CA, "hub-ca", "", "PEM CA file")
	flag.StringVar(&c.HubTLS.ClientCert, "hub-client-cert", "", "PEM client certificate")
	flag.StringVar(&c.HubTLS.ClientKey, "hub-client-key", "", "private PEM key")
	flag.Parse()
	c.Events = os.Stderr
	c.OnError = func(err error) { log.Printf("reconcile: %v", err) }
	c.OnSync = func(r receiver.Result) { log.Printf("%s: %s", r.Release, r.Phase) }
	a, err := receiver.New(c)
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
