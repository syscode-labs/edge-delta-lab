// Package publisher embeds the registry watcher and signed publication pipeline.
// Publication writes a locally mounted hub root; this is not a remote upload API.
package publisher

import (
	"context"
	"crypto/ed25519"
	"errors"

	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/registry"
	"os"
	"sync/atomic"
	"time"
)

// Repository selects tags. Ignore wins over Allow; both are regular expressions.
type Repository struct{ Name, Allow, Ignore string }

// Config is copied by New, including Repositories. Root, StateFile and
// SequenceFile require a single writer across processes. Event runs synchronously
// for lifecycle/retry errors; nil disables logging. PasswordEnv is read at Run.
// Registry traffic uses system TLS trust and never receives hub credentials.
type Config struct {
	RegistryURL                                                       string
	Repositories                                                      []Repository
	Poll                                                              time.Duration
	StateFile, Root, SigningKey, Channel, SequenceFile, ReleasePrefix string
	Username, PasswordEnv                                             string

	Event func(kind, detail string)
}

// Agent does not own the hub service. Run is blocking and non-concurrent.
type Agent struct {
	config  registry.Config
	event   func(string, string)
	running atomic.Bool
}

func New(c Config) (*Agent, error) {
	cfg := registry.Config{RegistryURL: c.RegistryURL, Poll: c.Poll, StateFile: c.StateFile, Username: c.Username, PasswordEnv: c.PasswordEnv,
		Publish: registry.PublishConfig{Root: c.Root, Key: c.SigningKey, Channel: c.Channel, SequenceFile: c.SequenceFile, ReleasePrefix: c.ReleasePrefix}}
	for _, r := range c.Repositories {
		cfg.Repos = append(cfg.Repos, registry.RepoConfig{Name: r.Name, Allow: r.Allow, Ignore: r.Ignore})
	}
	cfg, err := registry.ValidateConfig(cfg)
	if err != nil {
		return nil, err
	}

	if _, err := lab.ReadKey(cfg.Publish.Key, ed25519.PrivateKeySize); err != nil {
		return nil, err
	}
	return &Agent{config: cfg, event: c.Event}, nil
}

// Run retries scan/publication errors with the existing watcher's capped backoff
// and reports them through Event. Startup/state errors return directly; normal
// shutdown returns ctx.Err(). Local archive/signing work finishes its safe commit
// before cancellation is observed. Call again after return to resume durable state.
func (a *Agent) Run(ctx context.Context) error {
	if !a.running.CompareAndSwap(false, true) {
		return errors.New("publisher already running")
	}
	defer a.running.Store(false)
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg := a.config
	password := os.Getenv(cfg.PasswordEnv)
	if cfg.PasswordEnv != "" && password == "" {
		return errors.New("publisher password environment variable is not set")
	}
	client, err := registry.NewClient(cfg.RegistryURL, registry.AuthConfig{Username: cfg.Username, Password: password})
	if err != nil {
		return err
	}
	defer client.HTTP.CloseIdleConnections()
	watcher, err := registry.NewWatcher(client, cfg, registry.WithEventSink(a.event))
	if err != nil {
		return err
	}
	return watcher.Run(ctx, func(ctx context.Context, req registry.PublishRequest) error {
		_, err := registry.Trigger(ctx, registry.TriggerOptions{Config: cfg, Client: client, Event: a.event}, req)
		return err
	})
}
