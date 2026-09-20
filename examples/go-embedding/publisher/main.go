// Publisher is a standalone embedding example; the hub is a separate process.
package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"example.com/edge-delta-lab/publisher"
)

func main() {
	registryURL := flag.String("registry", "https://registry.example.test", "registry URL")
	root := flag.String("root", "work/origin", "local shared hub root")
	state := flag.String("state", "work/publisher", "single-writer state directory")
	key := flag.String("key", "work/keys/publisher.key", "signing key")
	repo := flag.String("repo", "app", "repository to watch")

	flag.Parse()
	a, err := publisher.New(publisher.Config{RegistryURL: *registryURL, Repositories: []publisher.Repository{{Name: *repo}},
		Root: *root, StateFile: filepath.Join(*state, "watch.json"), SequenceFile: filepath.Join(*state, "sequence"), SigningKey: *key, Channel: "desired",

		Event: func(kind, detail string) { log.Printf("%s: %s", kind, detail) }})
	if err != nil {
		log.Fatal(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := a.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}
