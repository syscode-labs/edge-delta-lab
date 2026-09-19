package clientconf

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"example.com/edge-delta-lab/internal/lab"
	"example.com/edge-delta-lab/internal/notify"
)

// Runner executes config-gated actions. The v2 invariant is preserved: lab.Sync
// with DockerLoad=true performs EVERY verification (signature, chunk hashes,
// sizes, whole-archive SHA-256) before the first Docker call, so a `load` run
// reaches Docker only after full verification. `restart` then works only on
// containers whose image ID is one of the manifest's signed image IDs.
type Runner struct {
	Config Config
	Events io.Writer // NDJSON lifecycle events
	Notify *notify.Dispatcher
}

func (r *Runner) event(kind, detail string) {
	if r.Events != nil {
		b, _ := json.Marshal(map[string]string{"event": kind, "detail": detail})
		r.Events.Write(append(b, '\n'))
	}
	if r.Notify != nil {
		kindMap := map[string]string{
			"release-eligible":   notify.KindReleasePublished,
			"release-ignored":    notify.KindReleasePublished,
			"delivery-staged":    notify.KindDeliveryStaged,
			"delivery-loaded":    notify.KindDeliveryLoaded,
			"containers-restart": notify.KindDeliveryLoaded,
			"delivery-failed":    notify.KindDeliveryFailed,
		}
		if mapped, ok := kindMap[kind]; ok {
			r.Notify.Dispatch(notify.Event{Kind: mapped, Detail: detail, Time: time.Now().UTC()})
		}
	}
}

// SyncOptions are the sync parameters derived from one config release scan.
type SyncOptions struct {
	// Release is the already-downloaded release being acted on. Actions are
	// per-release: a config with allow/ignore may stage several releases.
	Release string
	// ImageIDs are the manifest's signed image IDs (empty for raw archives).
	ImageIDs []string
	// Artifact is the assembled archive path from lab.Sync (state/staged).
	Artifact string
	// Phase is the sync result phase: "staged" (dry-run path) or "loaded".
	Phase string
}

// LoadOptions carries what lab.Sync needs, translated from the config.
// The runner never modifies lab semantics; it only fills AgentOptions.
func (c Config) LoadOptions(o *lab.AgentOptions) {
	o.ManifestURL = c.Manifest
	o.BaseURL = c.Origin
	o.StateDir = c.StateDir
	o.PublicKey = c.PubKey
	if c.DeviceID != "" {
		o.DeviceID = c.DeviceID
	}
	o.AllowHTTP = c.AllowHTTP
	if c.Workers > 0 {
		o.Workers = c.Workers
	}
}

// Act decides and executes the post-sync action for one release.
// phase must be "staged" (verified archive on disk, no Docker call yet) or
// "loaded" (lab.Sync already imported and identity-proved the image).
// imageIDs must come from the signed manifest, never from the network.
func (r *Runner) Act(ctx context.Context, so SyncOptions) (Plan, error) {
	c := r.Config
	p := Plan{Release: so.Release, Eligible: c.Eligible(so.Release), Action: c.Action}
	if !p.Eligible {
		p.Reason = "release filtered by allow/ignore"
		r.event("release-ignored", "release="+so.Release+" reason=allow-ignore-gate")
		return p, nil
	}
	switch c.Action {
	case ActionDryRun:
		p.WouldDo = []string{
			"docker image load --input " + so.Artifact,
			"verify stored image IDs against signed manifest image_ids",
		}
		if len(so.ImageIDs) > 0 {
			p.WouldDo = append(p.WouldDo, "restart containers using image "+strings.Join(so.ImageIDs, ", "))
		}
		p.Reason = "dry-run: nothing was touched"
		return p, nil
	case ActionLoad:
		if so.Phase != "loaded" {
			p.Reason = "archive verified and staged; load was not requested via --docker-load"
			p.WouldDo = []string{"re-run with docker-load enabled to import " + so.Artifact}
			return p, nil
		}
		p.WouldDo = []string{"docker image load (verified before import); stored image IDs proved against signed manifest"}
		r.event("delivery-loaded", "release="+so.Release)
		return p, nil
	case ActionRestart:
		if so.Phase != "loaded" {
			p.Reason = "restart requires a completed load; archive is staged only"
			return p, nil
		}
		if len(so.ImageIDs) == 0 {
			return p, fmt.Errorf("restart: manifest carries no signed image IDs")
		}
		recs, err := r.RestartMatching(ctx, so.ImageIDs)
		if err != nil {
			r.event("delivery-failed", "release="+so.Release+" stage=restart detail="+err.Error())
			return p, err
		}
		for _, rec := range recs {
			b, _ := json.Marshal(rec)
			p.WouldDo = append(p.WouldDo, string(b))
		}
		r.event("containers-restart", "release="+so.Release+" containers="+fmt.Sprint(len(recs)))
		return p, nil
	default:
		return p, fmt.Errorf("unknown action %q", c.Action)
	}
}

// AuditRecord is one per-container restart record, written to
// <state>/restart-audit/<release>.json and included in the action plan.
type AuditRecord struct {
	Container   string   `json:"container"`
	Image       string   `json:"image"`
	ImageID     string   `json:"image_id"`
	Action      string   `json:"action"`
	Recreated   bool     `json:"recreated"`
	OldNames    []string `json:"old_names,omitempty"`
	NewName     string   `json:"new_name,omitempty"`
	Error       string   `json:"error,omitempty"`
	StopTimeout int      `json:"stop_timeout"`
}

// RestartMatching maps image IDs to running containers via the configured
// docker CLI and recreates each match. Never called before a verified load.
func (r *Runner) RestartMatching(ctx context.Context, imageIDs []string) ([]AuditRecord, error) {
	cli := r.Config.Restart.DockerCLI
	var records []AuditRecord
	var includeRe, excludeRe *regexp.Regexp
	var err error
	if r.Config.Restart.Include != "" {
		if includeRe, err = regexp.Compile(r.Config.Restart.Include); err != nil {
			return nil, err
		}
	}
	if r.Config.Restart.Exclude != "" {
		if excludeRe, err = regexp.Compile(r.Config.Restart.Exclude); err != nil {
			return nil, err
		}
	}
	for _, imageID := range imageIDs {
		out, err := output(ctx, cli, "ps", "--filter", "ancestor="+imageID, "--format", "{{.ID}} {{.Names}} {{.Image}}")
		if err != nil {
			return records, fmt.Errorf("restart: docker ps: %w", err)
		}
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line == "" {
				continue
			}
			parts := strings.SplitN(line, " ", 3)
			cid := parts[0]
			name := cid
			imageName := cid
			if len(parts) > 1 {
				name = parts[1]
			}
			if len(parts) > 2 {
				imageName = parts[2]
			}
			if excludeRe != nil && excludeRe.MatchString(name) {
				continue
			}
			if includeRe != nil && !includeRe.MatchString(name) {
				continue
			}
			// ImageID is the SIGNED manifest image ID, not the docker-ps
			// label: the audit must state exactly which verified image the
			// replacement container is created from.
			rec := AuditRecord{Container: cid, Image: imageName, ImageID: imageID, Action: "recreate", StopTimeout: r.Config.Restart.StopTimeout}
			newName := name + "-restarted"
			if err := r.recreate(ctx, cli, cid, name, newName, imageID); err != nil {
				rec.Error = err.Error()
			} else {
				rec.Recreated = true
				rec.NewName = newName
			}
			records = append(records, rec)
		}
	}
	if err := r.WriteAudit(records); err != nil {
		return records, err
	}
	return records, nil
}

// recreate stops one container, removes it, and starts a replacement with the
// signed image ID and a new name. Audit-first: the record is returned
// regardless of outcome; a failed step is recorded, never retried silently.
func (r *Runner) recreate(ctx context.Context, cli, cid, name, newName, imageID string) error {
	timeout := fmt.Sprintf("--time=%d", r.Config.Restart.StopTimeout)
	if out, err := output(ctx, cli, "stop", timeout, cid); err != nil {
		return fmt.Errorf("stop %s: %w: %s", cid, err, strings.TrimSpace(out))
	}
	if out, err := output(ctx, cli, "rm", cid); err != nil {
		return fmt.Errorf("rm %s: %w: %s", cid, err, strings.TrimSpace(out))
	}
	if out, err := output(ctx, cli, "run", "--detach", "--name", newName, imageID); err != nil {
		return fmt.Errorf("run replacement %s: %w: %s", newName, err, strings.TrimSpace(out))
	}
	return nil
}

// WriteAudit persists per-container records under the state dir.
func (r *Runner) WriteAudit(records []AuditRecord) error {
	dir := filepath.Join(r.Config.StateDir, "restart-audit")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("restart-%d.json", timeNowUnix()))
	b, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// timeNowUnix is a small indirection point for audit timestamps.
func timeNowUnix() int64 { return time.Now().Unix() }

// output runs the docker CLI (or a fake in tests) and captures stdout.
func output(ctx context.Context, cli string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, cli, args...)
	var buf strings.Builder
	cmd.Stdout = &buf
	cmd.Stderr = io.Discard
	err := cmd.Run()
	return buf.String(), err
}
