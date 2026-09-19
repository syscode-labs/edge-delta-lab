package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"example.com/edge-delta-lab/internal/admin"
	"example.com/edge-delta-lab/internal/hub"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/notify"
	"example.com/edge-delta-lab/internal/push"
	"example.com/edge-delta-lab/internal/registry"
)

func fatal(e error) { fmt.Fprintln(os.Stderr, e); os.Exit(1) }

// hubStatsSafe returns the hub snapshot or an empty object when hub mode is
// off, so admin `stats` stays well-formed in every serve configuration.
func hubStatsSafe(f func() any) any {
	if f == nil {
		return map[string]any{}
	}
	return f()
}

// watcherStatus is the serve-process watch-status source. A serve process does
// not own a registry watcher; watch-registry processes feed their own state
// via their own admin socket. Empty by default, populated in-process when the
// trigger wiring registers one.
var watcherStatus = func() []admin.WatchStatusEntry { return nil }

func printJSON(v any) {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	if err := e.Encode(v); err != nil {
		fatal(err)
	}
}
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	fs := flag.NewFlagSet(cmd, flag.ExitOnError)
	switch cmd {
	case "keygen":
		out := fs.String("out", "keys", "key directory")
		fs.Parse(os.Args[2:])
		if e := lab.Keygen(*out); e != nil {
			fatal(e)
		}
	case "fixtures":
		out := fs.String("out", "work/fixtures", "output directory")
		size := fs.Int("size-mib", 16, "uncompressed image payload MiB")
		patch := fs.Int("patch-kib", 32, "changed bytes in the large app layer, KiB")
		fs.Parse(os.Args[2:])
		v, e := lab.Fixtures(*out, *size, *patch)
		if e != nil {
			fatal(e)
		}
		printJSON(v)
	case "publish":
		var o lab.PublishOptions
		fs.StringVar(&o.Input, "input", "", "uncompressed archive/file")
		fs.StringVar(&o.Root, "root", "work/origin", "origin directory")
		fs.StringVar(&o.Key, "key", "work/keys/publisher.key", "publisher private key")
		fs.StringVar(&o.Release, "release", "", "immutable release name")
		fs.Uint64Var(&o.Sequence, "sequence", 0, "monotonically increasing device-channel sequence")
		fs.StringVar(&o.Kind, "kind", "raw", "raw or docker-archive")
		fs.StringVar(&o.Source, "source", "", "source image digest/provenance")
		ids := fs.String("image-ids", "", "comma-separated expected Docker config IDs")
		min := fs.Int("min-kib", 16, "minimum CDC chunk KiB")
		avg := fs.Int("target-kib", 64, "CDC boundary target KiB (power of two)")
		max := fs.Int("max-kib", 256, "maximum CDC chunk KiB")
		fs.Parse(os.Args[2:])
		o.Min = *min << 10
		o.Avg = *avg << 10
		o.Max = *max << 10
		if *ids != "" {
			o.ImageIDs = strings.Split(*ids, ",")
		}
		m, e := lab.Publish(o)
		if e != nil {
			fatal(e)
		}
		printJSON(m)
	case "promote":
		root := fs.String("root", "work/origin", "origin directory")
		release := fs.String("release", "", "signed immutable release")
		channel := fs.String("channel", "desired", "mutable channel file")
		fs.Parse(os.Args[2:])
		if strings.ContainsAny(*release+*channel, "/\\") || *release == "" || *channel == "" || *release == *channel {
			fatal(fmt.Errorf("invalid release/channel"))
		}
		b, e := os.ReadFile(filepath.Join(*root, "releases", *release+".json"))
		if e != nil {
			fatal(e)
		}
		if e = lab.AtomicWrite(filepath.Join(*root, "releases", *channel+".json"), b, 0644); e != nil {
			fatal(e)
		}
	case "serve":
		root := fs.String("root", "work/origin", "read-only published object directory")
		addr := fs.String("listen", "127.0.0.1:8080", "listen address")
		faults := fs.String("faults", "", "JSON fault plan, re-read on each request")
		receipts := fs.String("receipts-dir", "", "sender persistence for outbound device acknowledgements (isolated lab only)")
		rate := fs.Int64("rate-kbit", 5000, "aggregate body rate in decimal kbit/s; 0 is unlimited")
		events := fs.Bool("events", false, "hub mode: expose the /events websocket announce channel (announces on releases/*.json writes)")
		cacheMib := fs.Int("cache-mib", 128, "hub object cache budget MiB (hub mode only; 0 disables caching)")
		adminSocket := fs.String("admin-socket", "", "unix socket path for the admin NDJSON endpoint (stats/clients/receipts/events/watch-status)")
		hubMode := fs.Bool("hub", false, "enable hub multiplexing + cache without the /events endpoint")
		telegramTokenEnv := fs.String("telegram-token-env", "", "env var holding a Telegram bot token (enables the Telegram notifier)")
		telegramChat := fs.String("telegram-chat", "", "Telegram chat id for the notifier")
		slackWebhookEnv := fs.String("slack-webhook-env", "", "env var holding a Slack webhook URL (enables the Slack notifier)")
		eventLogPath := fs.String("event-log", "", "durable admin event JSONL path (default <state>/events.jsonl when --admin-socket is set)")
		eventLogMib := fs.Int("event-log-mib", 8, "event log generation cap in MiB")
		fs.Parse(os.Args[2:])
		s := lab.NewFaultServer(*root, lab.FaultPlan{RateKbit: *rate})
		s.PlanFile = *faults
		s.ReceiptsDir = *receipts
		// Hub wiring (multiplex + cache + websocket + announce trigger) is
		// hoisted out of the old `if *events` so --admin-socket alone gets
		// full stats/clients visibility. /events stays gated on --events.
		broker := push.NewBroker()
		h := hub.New(func(rel string) ([]byte, error) {
			return os.ReadFile(filepath.Join(*root, filepath.FromSlash(rel)))
		}, *cacheMib<<20)
		var bus *admin.EventLog
		if *events || *hubMode {
			if *events {
				s.Events = broker.Handler()
			}
			s.ObjectLoader = h.Loader()
			s.DeviceHeader = "X-Edgelab-Device"
			s.ServeObserver = h.Record
		}
		// Announce trigger runs in hub mode: it watches releases/*.json and
		// announces via the broker + invalidates the hub cache. This is
		// write-only observation of the publish directory; /events exposure
		// is the separate --events gate.
		var hubStats func() any
		if *events || *hubMode {
			trig := push.NewTrigger(push.TriggerOptions{
				Root:     *root,
				Broker:   broker,
				Channels: []string{"desired"},
				Event: func(kind, detail string) {
					if kind == "announce" {
						if n := h.InvalidatePrefix("releases/"); n > 0 {
							detail += fmt.Sprintf(" cache_invalidated=%d", n)
						}
					}
					if bus != nil {
						_ = bus.Emit("hub-"+kind, detail)
					}
					_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": kind, "detail": detail, "component": "announce-trigger"})
				},
			})
			go trig.Run(ctx)
			hubStats = func() any { return h.Snapshot() }
			fmt.Fprintln(os.Stderr, "hub mode: announces active; hub cache", *cacheMib, "MiB")
		}
		// Admin socket + durable event log: observer plane only. The bus gets
		// hub trigger events above and origin stats snapshots below; notifiers
		// (when configured) receive release lifecycle events fan-out style.
		if *adminSocket != "" {
			logPath := *eventLogPath
			if logPath == "" {
				logPath = filepath.Join(filepath.Dir(*adminSocket), "events.jsonl")
			}
			// Assign the outer bus (captured by the announce-trigger
			// closure above); a := here would shadow it and the trigger
			// would emit into a nil bus forever.
			var e error
			bus, e = admin.OpenEventLog(logPath, int64(*eventLogMib)<<20)
			if e != nil {
				fatal(e)
			}
			defer bus.Close()
			var notifiers []notify.Notifier
			if *telegramTokenEnv != "" {
				if tk := os.Getenv(*telegramTokenEnv); tk != "" && *telegramChat != "" {
					notifiers = append(notifiers, notify.NewTelegram(tk, *telegramChat))
				}
			}
			if *slackWebhookEnv != "" {
				if wh := os.Getenv(*slackWebhookEnv); wh != "" {
					notifiers = append(notifiers, notify.NewSlack(wh))
				}
			}
			disp := notify.NewDispatcher(64, notifiers...)
			defer disp.Close()
			// Feed release lifecycle events from the durable bus into the
			// notifier dispatcher when one is configured.
			if len(notifiers) > 0 {
				bus.OnEmit(func(ev admin.Event) {
					if ev.Kind == "hub-announce" || ev.Kind == "hub-publish" || ev.Kind == "hub-promote" {
						disp.Dispatch(notify.Event{Kind: notify.KindReleasePublished, Detail: ev.Detail})
					}
				})
			}
			snapTick := time.NewTicker(10 * time.Second)
			defer snapTick.Stop()
			reactor := func(rctx context.Context) {
				for {
					select {
					case <-rctx.Done():
						return
					case <-snapTick.C:
						_ = bus.Emit("stats-snapshot", fmt.Sprintf("requests=%d chunk_requests=%d bytes=%d", s.Snapshot().Requests, s.Snapshot().ChunkRequests, s.Snapshot().ChunkBodyBytes))
					}
				}
			}
			asrv := admin.NewServer(admin.ServerOptions{
				Log:     bus,
				Stats:   func() any { return map[string]any{"origin": s.Snapshot(), "hub": hubStatsSafe(hubStats)} },
				Watch:   func() []admin.WatchStatusEntry { return watcherStatus() },
				Reactor: reactor,
			})
			go func() {
				if e := asrv.Serve(ctx, *adminSocket); e != nil {
					_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": "admin_server_error", "detail": e.Error()})
				}
			}()
			fmt.Fprintln(os.Stderr, "admin socket at", *adminSocket, "(event log", logPath+")")
		}
		fmt.Fprintln(os.Stderr, "origin listening at", *addr)
		if e := lab.Serve(ctx, *addr, s); e != nil {
			fatal(e)
		}
	case "sync", "watch":
		o := lab.DefaultAgentOptions()
		fs.StringVar(&o.ReceiptURL, "receipt-url", "", "outbound sender acknowledgement endpoint; no receiver listener")
		fs.StringVar(&o.DeviceID, "device-id", "edge-01", "device label in delivery acknowledgements")
		fs.StringVar(&o.ManifestURL, "manifest", "", "signed release/channel URL")
		fs.StringVar(&o.BaseURL, "base", "", "origin base URL")
		fs.StringVar(&o.StateDir, "state", "work/edge", "persistent edge directory")
		fs.StringVar(&o.PublicKey, "pub", "work/keys/publisher.pub", "pinned publisher public key")
		fs.IntVar(&o.Workers, "workers", o.Workers, "concurrent chunk requests")
		fs.IntVar(&o.MaxAttempts, "attempts", o.MaxAttempts, "per-object attempts; 0 unlimited")
		fs.DurationVar(&o.Backoff, "backoff", o.Backoff, "initial retry delay")
		fs.DurationVar(&o.MaxBackoff, "max-backoff", o.MaxBackoff, "retry cap")
		fs.DurationVar(&o.RequestTimeout, "request-timeout", o.RequestTimeout, "per-request deadline, including body; increase for very slow links")
		fs.BoolVar(&o.AllowHTTP, "allow-http", false, "allow plaintext only in an isolated lab")
		fs.BoolVar(&o.Offline, "offline", false, "use current authenticated cached manifest; no HTTP")
		fs.BoolVar(&o.DockerLoad, "docker-load", false, "explicitly import into the local Docker daemon; does not activate containers")
		max := fs.Int64("max-artifact-gib", 10, "maximum authorized artifact size")
		reserve := fs.Int64("reserve-mib", 64, "disk safety reserve")
		deadline := fs.Duration("deadline", 0, "overall command deadline; 0 unlimited")
		poll := fs.Duration("poll", 30*time.Second, "watch reconciliation interval")
		eventsURL := fs.String("events-url", "", "hub ws://.../events announce endpoint; verified hints trigger immediate reconcile (watch)")
		clientAdmin := fs.String("admin-socket", "", "client telemetry admin Unix socket (watch mode; never TCP)")
		fs.Parse(os.Args[2:])
		o.MaxArtifact = *max << 30
		o.ReserveBytes = *reserve << 20
		o.Events = os.Stderr
		o.Telemetry = lab.NewTelemetry()
		if *clientAdmin != "" {
			asrv := admin.NewServer(admin.ServerOptions{Stats: func() any { return map[string]any{"client": o.Telemetry.Snapshot()} }})
			go func() {
				if e := asrv.Serve(ctx, *clientAdmin); e != nil && !errors.Is(e, context.Canceled) {
					_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": "client_admin_error", "detail": e.Error()})
				}
			}()
		}
		if *deadline > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, *deadline)
			defer cancel()
		}
		kick := make(chan struct{}, 1)
		if *eventsURL != "" {
			pub, e := lab.ReadKey(o.PublicKey, ed25519.PublicKeySize)
			if e != nil {
				fatal(fmt.Errorf("events-url requires the pinned public key: %w", e))
			}
			// Announcements are hints only: the envelope is re-verified against
			// the pinned key here — the transport is never trusted — and a
			// valid hint merely wakes the existing reconcile loop early. The
			// signed manifest fetched by Sync remains the only source of truth.
			go push.Run(ctx, push.ClientOptions{
				URL:    *eventsURL,
				Device: o.DeviceID,
				OnAnnounce: func(a push.Announce) {
					m, err := push.Accept(a, ed25519.PublicKey(pub), o.MaxArtifact)
					if err != nil {
						_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": "announce_rejected", "error": err.Error()})
						return
					}
					_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": "announce_accepted", "release": m.Release, "sequence": m.Sequence})
					select {
					case kick <- struct{}{}:
					default:
					}
				},
			})
		}
		for {
			s, e := lab.Sync(ctx, o)
			if e == nil {
				e = lab.WriteSummary(o.StateDir, s)
			}
			if cmd == "sync" {
				if e != nil {
					fatal(e)
				}
				printJSON(s)
				return
			}
			if e != nil {
				_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": "reconcile_failed", "error": e.Error()})
			} else {
				printJSON(s)
			}
			if *poll <= 0 {
				fatal(fmt.Errorf("poll must be positive"))
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(*poll):
			case <-kick: // verified announcement: reconcile immediately
			}
		}
	case "watch-registry":
		cfgPath := fs.String("config", "", "watch-registry YAML config path (required)")
		fs.Parse(os.Args[2:])
		if *cfgPath == "" {
			fatal(fmt.Errorf("watch-registry: -config is required"))
		}
		cfg, e := registry.LoadConfig(*cfgPath)
		if e != nil {
			fatal(e)
		}
		password := ""
		if cfg.PasswordEnv != "" {
			password = os.Getenv(cfg.PasswordEnv)
			if password == "" {
				fatal(fmt.Errorf("watch-registry: %s is not set", cfg.PasswordEnv))
			}
		}
		client, e := registry.NewClient(cfg.RegistryURL, registry.AuthConfig{Username: cfg.Username, Password: password})
		if e != nil {
			fatal(e)
		}
		w, e := registry.NewWatcher(client, cfg, registry.WithEventSink(func(kind, detail string) {
			_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": kind, "detail": detail})
		}))
		if e != nil {
			fatal(e)
		}
		fmt.Fprintln(os.Stderr, "watch-registry: polling", cfg.RegistryURL, "for", len(cfg.Repos), "repo(s)")
		if e := w.Run(ctx, func(c context.Context, req registry.PublishRequest) error {
			_, err := registry.Trigger(c, registry.TriggerOptions{
				Config: cfg,
				Event: func(kind, detail string) {
					_ = json.NewEncoder(os.Stderr).Encode(map[string]any{"event": kind, "detail": detail})
				},
				Client: client,
			}, req)
			return err
		}); e != nil && !errors.Is(e, context.Canceled) {
			fatal(e)
		}
	case "conf":
		if err := runConf(ctx, os.Args[2:]); err != nil {
			fatal(err)
		}
	case "status":
		dir := fs.String("state", "work/edge", "edge directory")
		fs.Parse(os.Args[2:])
		b, e := os.ReadFile(filepath.Join(*dir, "state.json"))
		if e != nil {
			fatal(e)
		}
		fmt.Println(string(b))
	case "help", "--help", "-h":
		usage()
	default:
		usage()
		os.Exit(2)
	}
}
func usage() {
	fmt.Println(`edge-delta-lab: signed, content-defined chunk transport for an intermittent edge

Commands:
  keygen     Generate a local publisher signing key and edge trust key
  fixtures   Make reproducible layered Docker-shaped test archives
  publish    Chunk an artifact, compress chunks, sign an immutable release
  promote    Point a channel at an existing signed release
  serve      Serve objects with throttling, disconnects, stalls and outages
  sync       Reconcile once, durably stage, optionally docker load
  watch      Reconcile continuously with persistent state
  watch-registry  Poll a Docker registry and publish on new/changed tags
  conf       Config-gated client run from a YAML file (verify manifest, allow/ignore gate, dry-run|load|restart)
  edgelab-tui     Live admin console over the unix socket (stats/events/watch)
  status     Read persisted release state

Run: edgelab <command> -h for flags. HTTP is opt-in; Docker is never implicit.
This is an independent lab format, not a desync implementation.`)
}
