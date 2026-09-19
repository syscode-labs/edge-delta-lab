// ConfCommand implements `edgelab conf`: the v3 config-gated client flow.
//
// Order of operations is the safety story:
//
//  1. Parse+validate the YAML config (defaults applied; action default dry-run).
//  2. Fetch and VERIFY the signed manifest with the pinned key. The manifest is
//     the only source of truth for release name, sequence, chunks and image IDs.
//  3. Apply the allow/ignore gate BEFORE any download: an ineligible release
//     never costs bytes.
//  4. Sync the artifact (staged). DockerLoad is enabled ONLY when the config
//     action is load or restart — dry-run never reaches Docker, and no other
//     mode can silently enable it.
//  5. Runner.Act decides the post-sync action per the config; restart works
//     only on containers whose image ID matches the manifest's signed IDs.
//
// The v2 invariants are untouched: signature, chunk, size and whole-archive
// verification all happen inside lab.Sync BEFORE the first Docker call.
package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"example.com/edge-delta-lab/internal/clientconf"
	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/notify"
)

// confHTTPGet fetches url honoring the lab URL policy (HTTPS unless the lab
// allow-http escape hatch is on). Returns the caller-closed body.
func confHTTPGet(o lab.AgentOptions, raw string) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("URLs require a host and must not include credentials, query or fragment")
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && o.AllowHTTP) {
		return nil, fmt.Errorf("HTTPS required; allow_http is for an isolated lab only")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d from %s", resp.StatusCode, raw)
	}
	return resp.Body, nil
}

// fetchVerifiedManifest downloads the signed envelope at url and verifies it
// against the pinned key. It is the only place conf learns release metadata;
// nothing is downloaded before this succeeds.
func fetchVerifiedManifest(o lab.AgentOptions, maxArtifact int64) (lab.Manifest, error) {
	resp, err := confHTTPGet(o, o.ManifestURL)
	if err != nil {
		return lab.Manifest{}, fmt.Errorf("manifest fetch: %w", err)
	}
	defer resp.Close()
	data, err := io.ReadAll(io.LimitReader(resp, lab.MaxManifestBytes+1))
	if err != nil {
		return lab.Manifest{}, err
	}
	if int64(len(data)) > lab.MaxManifestBytes {
		return lab.Manifest{}, fmt.Errorf("manifest exceeds limit")
	}
	pub, err := lab.ReadKey(o.PublicKey, ed25519.PublicKeySize)
	if err != nil {
		return lab.Manifest{}, fmt.Errorf("pinned public key: %w", err)
	}
	m, _, err := lab.Verify(data, ed25519.PublicKey(pub), maxArtifact)
	return m, err
}

func runConf(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("conf", flag.ExitOnError)
	cfgPath := fs.String("config", "", "client YAML config path (required)")
	maxGib := fs.Int64("max-artifact-gib", 10, "maximum authorized artifact size")
	fs.Parse(args)
	if *cfgPath == "" {
		return fmt.Errorf("conf: -config is required")
	}

	cfg, err := clientconf.Load(*cfgPath)
	if err != nil {
		return err
	}

	// Agent options: config fields map onto the unchanged v2 sync path. Push
	// wiring stays in the cmd layer by design (hub announce hints, not sync).
	maxArtifact := *maxGib << 30
	o := lab.DefaultAgentOptions()
	cfg.LoadOptions(&o)
	o.Events = os.Stderr
	o.MaxArtifact = maxArtifact

	// Notifications are opt-in via the config; dry-run still emits lifecycle
	// events but never touches Docker.
	var disp *notify.Dispatcher
	if len(cfg.Notifiers) > 0 {
		ns, err := buildNotifiers(cfg.Notifiers)
		if err != nil {
			return err
		}
		disp = notify.NewDispatcher(64, ns...)
		defer disp.Close()
	}
	runner := &clientconf.Runner{Config: cfg, Events: os.Stderr, Notify: disp}

	// Step: verify manifest FIRST (signature with pinned key), gate SECOND
	// (before any artifact byte is fetched).
	m, err := fetchVerifiedManifest(o, maxArtifact)
	if err != nil {
		return err
	}
	if !cfg.Eligible(m.Release) {
		p := clientconf.Plan{Release: m.Release, Eligible: false, Action: cfg.Action, Reason: "release filtered by allow/ignore (gated before download)"}
		return printPlans([]clientconf.Plan{p})
	}

	// Sync. DockerLoad only when the action demands a real import; dry-run
	// and gate-failures never reach this with DockerLoad=true.
	o.DockerLoad = cfg.Action != clientconf.ActionDryRun
	s, err := lab.Sync(ctx, o)
	if err != nil {
		_ = runner.WriteAudit(nil) // no records; audit dir proves the run
		return fmt.Errorf("sync %s: %w", m.Release, err)
	}
	if err := lab.WriteSummary(o.StateDir, s); err != nil {
		return err
	}

	// Signed image IDs come from the verified manifest only.
	so := clientconf.SyncOptions{
		Release:  m.Release,
		ImageIDs: m.ImageIDs,
		Artifact: s.Artifact,
		Phase:    s.Phase,
	}
	plan, err := runner.Act(ctx, so)
	if err != nil {
		return err
	}
	return printPlans([]clientconf.Plan{plan})
}

func printPlans(plans []clientconf.Plan) error {
	clientconf.SortPlans(plans)
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(plans)
}

// buildNotifiers constructs dispatchers from config entries. Tokens come from
// the environment only (token_env / webhook_env); a missing env var disables
// that notifier loudly rather than sending to an empty endpoint.
func buildNotifiers(ns []clientconf.NotifierConfig) ([]notify.Notifier, error) {
	var out []notify.Notifier
	for _, n := range ns {
		switch strings.ToLower(n.Type) {
		case "telegram":
			tok := os.Getenv(n.TokenEnv)
			if tok == "" {
				return nil, fmt.Errorf("notifier telegram: env %s is not set", n.TokenEnv)
			}
			out = append(out, notify.NewTelegram(tok, n.ChatID))
		case "slack":
			hook := os.Getenv(n.WebhookEnv)
			if hook == "" {
				return nil, fmt.Errorf("notifier slack: env %s is not set", n.WebhookEnv)
			}
			out = append(out, notify.NewSlack(hook))
		default:
			return nil, fmt.Errorf("notifier type %q not supported (telegram|slack)", n.Type)
		}
	}
	return out, nil
}
