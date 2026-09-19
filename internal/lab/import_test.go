package lab

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This fake exercises the actual CLI boundary, not a real Docker daemon.
func importRig(t *testing.T) (*rig, func(string, uint64) Manifest, func(int)) {
	t.Helper()
	r := newRig(t, FaultPlan{})
	config, id := signedConfigBytes(t)
	archive := buildSaveArchive(t, "", config)
	bin := t.TempDir()
	script := `#!/bin/sh
set -eu
case "$2" in
load)
 printf 'load\n' >> "$IMPORT_LOG"
 [ "${IMPORT_MODE:-}" != fail ] || exit 1
 cp "$4" "$IMPORT_ARCHIVE"
 [ "${IMPORT_MODE:-}" != ambiguous ] || exit 0
 printf 'Loaded image: lab:test\n'
 ;;
save)
 [ "${IMPORT_MODE:-}" != proof-failure ] || exit 1
 cp "$IMPORT_ARCHIVE" "$4"
 ;;
*) exit 2 ;;
esac
`
	if e := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0700); e != nil {
		t.Fatal(e)
	}
	log := filepath.Join(bin, "imports")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("IMPORT_LOG", log)
	t.Setenv("IMPORT_ARCHIVE", filepath.Join(bin, "stored.tar"))
	t.Setenv("IMPORT_MODE", "")
	publish := func(name string, seq uint64) Manifest {
		t.Helper()
		m, e := Publish(PublishOptions{Input: archive, Root: r.root, Key: r.key, Release: name, Sequence: seq, Kind: "docker-archive", ImageIDs: []string{id}, Min: 1024, Avg: 4096, Max: 16384})
		if e != nil {
			t.Fatal(e)
		}
		return m
	}
	count := func(want int) {
		t.Helper()
		b, e := os.ReadFile(log)
		if e != nil && !os.IsNotExist(e) {
			t.Fatal(e)
		}
		if got := strings.Count(string(b), "load\n"); got != want {
			t.Fatalf("imports = %d, want %d", got, want)
		}
	}
	return r, publish, count
}

func syncImport(t *testing.T, r *rig, name string) Summary {
	t.Helper()
	o := r.options(name)
	o.DockerLoad = true
	s, e := Sync(context.Background(), o)
	if e != nil {
		t.Fatal(e)
	}
	if s.Phase != "loaded" {
		t.Fatalf("phase = %s", s.Phase)
	}
	return s
}

func TestDockerImportUnchangedAndRestart(t *testing.T) {
	r, publish, count := importRig(t)
	publish("v1", 1)
	syncImport(t, r, "v1")
	syncImport(t, r, "v1")
	count(1)
	o := r.options("v1")
	o.DockerLoad = true
	o.Events = nil
	b, e := json.Marshal(o)
	if e != nil {
		t.Fatal(e)
	}
	// A separate process has no in-memory import history.
	cmd := exec.Command(os.Args[0], "-test.run=^TestDockerImportRestartHelper$")
	cmd.Env = append(os.Environ(), "IMPORT_HELPER_OPTIONS="+string(b))
	if out, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("restart: %v: %s", e, out)
	}
	count(1)
	// Same image/archive but a different signed release must still import once.
	publish("v2", 2)
	syncImport(t, r, "v2")
	syncImport(t, r, "v2")
	count(2)
}

func TestDockerImportRetriesUnprovenCompletion(t *testing.T) {
	for _, mode := range []string{"fail", "ambiguous", "proof-failure"} {
		t.Run(mode, func(t *testing.T) {
			r, publish, count := importRig(t)
			publish("v1", 1)
			syncImport(t, r, "v1")
			publish("v2", 2)
			t.Setenv("IMPORT_MODE", mode)
			o := r.options("v2")
			o.DockerLoad = true
			if _, e := Sync(context.Background(), o); e == nil {
				t.Fatal("unproven import succeeded")
			}
			s, e := readState(r.state)
			if e != nil {
				t.Fatal(e)
			}
			if s.LoadedManifestSHA256 != "" || s.Phase != "loading" {
				t.Fatalf("ambiguous state: %+v", s)
			}
			count(2)
			t.Setenv("IMPORT_MODE", "")
			syncImport(t, r, "v2")
			syncImport(t, r, "v2")
			count(3)
		})
	}
}

func TestDockerImportRecoveryAndVerification(t *testing.T) {
	r, publish, count := importRig(t)
	m := publish("v1", 1)
	first := syncImport(t, r, "v1")
	// A corrupt chunk still fails offline despite a completed import and valid archive.
	c := Unique(m)[0]
	if e := os.WriteFile(CachePath(r.state, c), []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	o := r.options("v1")
	o.DockerLoad = true
	o.Offline = true
	if _, e := Sync(context.Background(), o); e == nil || !strings.Contains(e.Error(), "missing or corrupt") {
		t.Fatalf("corrupt cache accepted: %v", e)
	}
	count(1)
	// Planning persisted before the failed check must not erase import history.
	repaired := syncImport(t, r, "v1")
	if repaired.DownloadedChunks != 1 {
		t.Fatalf("not repaired: %+v", repaired)
	}
	count(1)
	// The staged archive is verified/reassembled, never trusted from import history.
	if e := os.WriteFile(first.Artifact, []byte("corrupt"), 0600); e != nil {
		t.Fatal(e)
	}
	syncImport(t, r, "v1")
	h, n, e := FileHash(first.Artifact)
	if e != nil || h != m.ArtifactSHA256 || n != m.ArtifactSize {
		t.Fatalf("archive not reverified: %s %d %v", h, n, e)
	}
	count(1)
	// A non-importing pass stays staged and preserves the completed import marker.
	if s, e := Sync(context.Background(), r.options("v1")); e != nil || s.Phase != "staged" {
		t.Fatalf("staging: %+v %v", s, e)
	}
	syncImport(t, r, "v1")
	count(1)
	// Legacy phase-only state has no exact proof and must conservatively import.
	s, e := readState(r.state)
	if e != nil {
		t.Fatal(e)
	}
	s.LoadedManifestSHA256 = ""
	if e := writeState(r.state, s); e != nil {
		t.Fatal(e)
	}
	syncImport(t, r, "v1")
	count(2)
}

func TestDockerImportHistoryDoesNotBypassTrust(t *testing.T) {
	r, publish, count := importRig(t)
	publish("v1", 1)
	syncImport(t, r, "v1")
	publish("other", 1)
	o := r.options("other")
	o.DockerLoad = true
	if _, e := Sync(context.Background(), o); e == nil || !strings.Contains(e.Error(), "equivocation") {
		t.Fatalf("equivocation: %v", e)
	}
	publish("v2", 2)
	syncImport(t, r, "v2")
	o = r.options("v1")
	o.DockerLoad = true
	if _, e := Sync(context.Background(), o); e == nil || !strings.Contains(e.Error(), "rollback") {
		t.Fatalf("rollback: %v", e)
	}
	// The loaded release itself must still authenticate on every reconciliation.
	path := filepath.Join(r.root, "releases", "v2.json")
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	var env Envelope
	if e := json.Unmarshal(b, &env); e != nil {
		t.Fatal(e)
	}
	env.Signature = strings.Repeat("A", 88)
	b, e = json.Marshal(env)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, b, 0600); e != nil {
		t.Fatal(e)
	}
	o = r.options("v2")
	o.DockerLoad = true
	if _, e := Sync(context.Background(), o); e == nil || !strings.Contains(e.Error(), "signature") {
		t.Fatalf("signature: %v", e)
	}
	count(2)
}

func TestDockerImportRestartHelper(t *testing.T) {
	raw := os.Getenv("IMPORT_HELPER_OPTIONS")
	if raw == "" {
		t.Skip("subprocess only")
	}
	var o AgentOptions
	if e := json.Unmarshal([]byte(raw), &o); e != nil {
		t.Fatal(e)
	}
	o.Events = nil
	if s, e := Sync(context.Background(), o); e != nil || s.Phase != "loaded" {
		t.Fatalf("sync: %+v %v", s, e)
	}
}
