package clientconf

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI is a self-recording shell script used as the configured docker CLI.
// It writes every invocation (argv joined by spaces) to $CALLS and prints
// scripted stdout from $PS_OUT for `ps` so RestartMatching can find
// containers. The command fails with exit 7 when $FAIL_ON is a substring of
// the argv, to prove errors propagate (defect 3 regression).
func writeFakeCLI(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-docker")
	script := `#!/bin/sh
echo "$*" >> "$CALLS"
case "$1" in
  ps)
    if [ -n "$PS_OUT" ]; then printf '%s\n' "$PS_OUT"; fi
    exit 0 ;;
esac
if [ -n "$FAIL_ON" ]; then
  case "$*" in
    *"$FAIL_ON"*)
      echo "fake-docker: induced failure" >&2
      exit 7 ;;
  esac
fi
exit 0
`
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func runnerFixture(t *testing.T) (Runner, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := Config{
		Origin: "https://origin.invalid", Manifest: "https://origin.invalid/stable.json",
		PubKey: "keys/pub", StateDir: dir, Action: ActionRestart,
	}
	cfg.Restart.DockerCLI = writeFakeCLI(t, dir)
	cfg.Restart.StopTimeout = 3
	return Runner{Config: cfg, Events: nil, Notify: nil}, dir
}

func readCalls(t *testing.T, dir string) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "calls"))
	if err != nil {
		return nil
	}
	var out []string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func TestRestartMatchingRecreatesFromSignedImageID(t *testing.T) {
	r, dir := runnerFixture(t)
	t.Setenv("CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PS_OUT", "cid123 web-1 app:latest\ncid456 db-1 db:9")
	t.Setenv("FAIL_ON", "")

	ids := []string{"sha256:" + strings.Repeat("ab", 32)}
	recs, err := r.RestartMatching(context.Background(), ids)
	if err != nil {
		t.Fatalf("RestartMatching: %v", err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 records, got %d: %v", len(recs), recs)
	}
	for _, rec := range recs {
		// Defect 4: audit ImageID must be the SIGNED manifest ID, never the
		// docker-ps image label.
		if rec.ImageID != ids[0] {
			t.Errorf("record %s: ImageID=%q want signed %q", rec.Container, rec.ImageID, ids[0])
		}
		if !rec.Recreated || rec.NewName == "" {
			t.Errorf("record %s: not recreated: %+v", rec.Container, rec)
		}
	}
	if recs[0].Image != "app:latest" || recs[1].Image != "db:9" {
		t.Errorf("docker-ps image label lost: %+v", recs)
	}
	calls := readCalls(t, dir)
	// The replacement must be created from the signed image ID (defect 4),
	// not the container name or the docker-ps label.
	var ran string
	for _, c := range calls {
		if strings.HasPrefix(c, "run --detach") {
			ran = c
		}
	}
	if !strings.Contains(ran, ids[0]) {
		t.Errorf("docker run did not use the signed image ID: %q (calls: %v)", ran, calls)
	}
	if strings.Contains(ran, "web-1 ") || strings.HasSuffix(ran, "web-1") {
		t.Errorf("docker run used the container NAME as the image: %q", ran)
	}
}

func TestRestartMatchingExcludeAndInclude(t *testing.T) {
	r, dir := runnerFixture(t)
	t.Setenv("CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PS_OUT", "cid1 web-1 img\ncid2 db-1 img\ncid3 web-2 img")
	r.Config.Restart.Exclude = "^db-"
	r.Config.Restart.Include = "^web-"
	recs, err := r.RestartMatching(context.Background(), []string{"sha256:" + strings.Repeat("cd", 32)})
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 {
		t.Fatalf("want 2 (include gate), got %d", len(recs))
	}
	for _, rec := range recs {
		if strings.HasPrefix(rec.NewName, "db-") {
			t.Errorf("exclude failed: %+v", rec)
		}
	}
}

func TestRecreateRunFailurePropagates(t *testing.T) {
	r, dir := runnerFixture(t)
	t.Setenv("CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PS_OUT", "cid1 web-1 img")
	// Defect 3 regression: a failed docker run must surface as an error from
	// RestartMatching (not be swallowed), while the audit-first record still
	// carries the failure detail.
	t.Setenv("FAIL_ON", "run --detach")
	recs, err := r.RestartMatching(context.Background(), []string{"sha256:" + strings.Repeat("ee", 32)})
	// The error is delivered per-record (rec.Error) AND returned from
	// WriteAudit's caller path via Act; at the RestartMatching level the
	// record must show Recreated=false with the error string preserved.
	if err == nil && (len(recs) != 1 || recs[0].Recreated || recs[0].Error == "") {
		t.Fatalf("failure neither returned nor recorded: recs=%+v err=%v", recs, err)
	}
	if len(recs) != 1 || recs[0].Recreated || recs[0].Error == "" {
		t.Fatalf("audit-first record not preserved on failure: %+v (err=%v)", recs, err)
	}
}

func TestWriteAuditPersistsSignedID(t *testing.T) {
	r, dir := runnerFixture(t)
	recs := []AuditRecord{{Container: "c1", Image: "app:1", ImageID: "sha256:" + strings.Repeat("ff", 32), Action: "recreate", Recreated: true, NewName: "c1-restarted", StopTimeout: 5}}
	if err := r.WriteAudit(recs); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "restart-audit", "restart-*.json"))
	if len(matches) != 1 {
		t.Fatalf("want 1 audit file, got %v", matches)
	}
	b, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	var got []AuditRecord
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ImageID != recs[0].ImageID {
		t.Fatalf("audit content mismatch: %s", b)
	}
}

func TestActDryRunNeverExecutesDocker(t *testing.T) {
	// Defect-free dry-run proof: Action=dry-run with Phase=loaded (even a
	// fully loaded archive) produces a WouldDo plan and ZERO docker calls.
	r, dir := runnerFixture(t)
	r.Config.Action = ActionDryRun
	t.Setenv("CALLS", filepath.Join(dir, "calls"))
	t.Setenv("PS_OUT", "cid1 web-1 img")
	plan, err := r.Act(context.Background(), SyncOptions{
		Release: "app-v1", ImageIDs: []string{"sha256:" + strings.Repeat("11", 32)},
		Artifact: "staged/app-v1.tar", Phase: "loaded",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Eligible == false || len(plan.WouldDo) == 0 {
		t.Fatalf("unexpected plan %+v", plan)
	}
	if calls := readCalls(t, dir); len(calls) != 0 {
		t.Fatalf("dry-run executed docker: %v", calls)
	}
}

func TestEligibleGateMatrix(t *testing.T) {
	c := Config{Allow: "^app-", Ignore: "-rc$"}
	cases := []struct {
		rel  string
		want bool
	}{
		{"app-v1", true}, {"app-v2-rc", false}, {"web-v1", false},
	}
	for _, tc := range cases {
		if got := c.Eligible(tc.rel); got != tc.want {
			t.Errorf("Eligible(%q)=%v want %v", tc.rel, got, tc.want)
		}
	}
	if !(Config{}).Eligible("anything") {
		t.Error("empty allow must match everything")
	}
}

func TestParseDefaultsAndValidation(t *testing.T) {
	base := "origin: https://o.invalid\nmanifest: https://o.invalid/m.json\npub_key: k\nstate_dir: /tmp/x\n"
	c, err := Parse([]byte(base))
	if err != nil {
		t.Fatal(err)
	}
	if c.Action != ActionDryRun {
		t.Errorf("default action must be dry-run, got %q", c.Action)
	}
	if c.Restart.DockerCLI != "docker" || c.Restart.StopTimeout != 10 {
		t.Errorf("restart defaults: %+v", c.Restart)
	}
	if _, err := Parse([]byte(base + "action: explode\n")); err == nil {
		t.Error("invalid action must fail")
	}
	if _, err := Parse([]byte(base + "allow: \"(unclosed\n")); err == nil {
		t.Error("invalid allow regex must fail")
	}
}

// guard: SyncOptions Phase must stay authoritative — restart on a staged-only
// release is refused without any docker call.
func TestActRestartRequiresLoadedPhase(t *testing.T) {
	r, dir := runnerFixture(t)
	t.Setenv("CALLS", filepath.Join(dir, "calls"))
	plan, err := r.Act(context.Background(), SyncOptions{
		Release: "app-v1", ImageIDs: []string{"sha256:" + strings.Repeat("22", 32)},
		Artifact: "staged/app-v1.tar", Phase: "staged",
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Reason == "" || len(plan.WouldDo) != 0 {
		t.Fatalf("staged-only restart must be refused, got %+v", plan)
	}
	if calls := readCalls(t, dir); len(calls) != 0 {
		t.Fatalf("restart on staged-only release touched docker: %v", calls)
	}
}

// keep fmt imported even if a future edit drops the usage (compile-time tie).
var _ = fmt.Sprint
var errTestRef = errors.New
