package lab

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// digestOf returns the sha256: form of the hash of b.
func digestOf(b []byte) string { return "sha256:" + Hash(b) }

// saveArchive models a Docker 29 containerd-style `docker image save` result:
// an OCI layout (index.json + blobs/sha256/*) plus the legacy manifest.json
// compat list. configOverride lets a test lie with the blob filename while the
// bytes differ, mirroring a store that labels by manifest digest.
type saveArchive struct {
	LayerRaw   []byte
	Config     []byte
	ConfigPath string // blob path written for the config, e.g. blobs/sha256/<hex>
}

func buildSaveArchive(t *testing.T, configPathOverride string, configBytes []byte) string {
	t.Helper()
	layerRaw := []byte("raw-layer-bytes")
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	if _, e := zw.Write(layerRaw); e != nil {
		t.Fatal(e)
	}
	if e := zw.Close(); e != nil {
		t.Fatal(e)
	}
	layerGz := gz.Bytes()
	config := configBytes
	configPath := configPathOverride
	if configPath == "" {
		configPath = "blobs/sha256/" + digestOf(config)[len("sha256:"):]
	}
	manifest, e := json.Marshal(map[string]any{
		"schemaVersion": 2,
		"config":        map[string]any{"digest": digestOf(config), "mediaType": "application/vnd.oci.image.config.v1+json"},
		"layers":        []any{map[string]any{"digest": digestOf(layerGz), "size": len(layerGz)}},
	})
	if e != nil {
		t.Fatal(e)
	}
	index, e := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": []any{
		map[string]any{"digest": digestOf(manifest), "mediaType": "application/vnd.oci.image.manifest.v1+json"},
	}})
	if e != nil {
		t.Fatal(e)
	}
	dockerList, e := json.Marshal([]map[string]any{{
		"Config":   configPath,
		"RepoTags": []string{"edge-delta-demo-000000000000:app-a-v1"},
	}})
	if e != nil {
		t.Fatal(e)
	}
	path := filepath.Join(t.TempDir(), "saved.tar")
	f, e := os.Create(path)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	members := []struct {
		name string
		body []byte
	}{
		{"oci-layout", []byte(`{"imageLayoutVersion":"1.0.0"}`)},
		{"blobs/sha256/" + digestOf(layerGz)[len("sha256:"):], layerGz},
		{configPath, config},
		{"blobs/sha256/" + digestOf(manifest)[len("sha256:"):], manifest},
		{"index.json", index},
		{"blobs/sha256/" + digestOf(index)[len("sha256:"):], index},
		{"manifest.json", dockerList},
	}
	for _, m := range members {
		if e := tw.WriteHeader(&tar.Header{Name: m.name, Mode: 0644, Size: int64(len(m.body))}); e != nil {
			t.Fatal(e)
		}
		if _, e := tw.Write(m.body); e != nil {
			t.Fatal(e)
		}
	}
	if e := tw.Close(); e != nil {
		t.Fatal(e)
	}
	return path
}

func signedConfigBytes(t *testing.T) ([]byte, string) {
	t.Helper()
	diffID := digestOf([]byte("raw-layer-bytes"))
	config, e := json.Marshal(map[string]any{
		"architecture": "amd64",
		"os":           "linux",
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{diffID}},
	})
	if e != nil {
		t.Fatal(e)
	}
	return config, digestOf(config)
}

func TestSavedConfigIDHashesPreservedConfigBytes(t *testing.T) {
	config, signed := signedConfigBytes(t)
	path := buildSaveArchive(t, "", config)
	got, e := savedConfigID(path)
	if e != nil {
		t.Fatalf("savedConfigID: %v", e)
	}
	if got != signed {
		t.Fatalf("preserved config bytes hashed to %s, signed identity is %s", got, signed)
	}
}

func TestSavedConfigIDRejectsTamperedConfigBytes(t *testing.T) {
	config, signed := signedConfigBytes(t)
	tampered, e := json.Marshal(map[string]any{
		"architecture": "arm64", // signed identity claims amd64
		"os":           "linux",
		"rootfs":       map[string]any{"type": "layers", "diff_ids": []string{digestOf([]byte("raw-layer-bytes"))}},
	})
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(tampered, config) {
		t.Fatal("tamper produced no change")
	}
	// The store blob filename still claims the signed digest; the returned
	// identity must follow the bytes, never the filename. Refusing the image
	// is the caller's signed-ID comparison (fail-closed coverage above).
	path := buildSaveArchive(t, "blobs/sha256/"+signed[len("sha256:"):], tampered)
	got, e := savedConfigID(path)
	if e != nil {
		t.Fatalf("byte-hash identity must not error on the filename claim: %v", e)
	}
	if got == signed {
		t.Fatal("blob filename label leaked into identity despite differing bytes")
	}
}

func TestSavedConfigIDIgnoresBlobFilenameLabels(t *testing.T) {
	// A containerd-backed store labels the archive by manifest digest; the
	// config blob path in the compat list must never be trusted as identity.
	config, signed := signedConfigBytes(t)
	path := buildSaveArchive(t, "blobs/sha256/"+strings.Repeat("ab", 32), config)
	got, e := savedConfigID(path)
	if e != nil {
		t.Fatalf("savedConfigID: %v", e)
	}
	if got != signed {
		t.Fatalf("store label leaked into identity: got %s want %s", got, signed)
	}
}

func TestSavedConfigIDFailsClosedOnBrokenArchives(t *testing.T) {
	config, _ := signedConfigBytes(t)
	if _, e := savedConfigID(filepath.Join(t.TempDir(), "missing.tar")); e == nil {
		t.Fatal("absent archive must fail")
	}
	empty := filepath.Join(t.TempDir(), "empty.tar")
	if e := os.WriteFile(empty, nil, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := savedConfigID(empty); e == nil {
		t.Fatal("archive without manifest.json must fail")
	}
	twoImages, e := json.Marshal([]map[string]any{
		{"Config": "blobs/sha256/" + strings.Repeat("aa", 32)},
		{"Config": "blobs/sha256/" + strings.Repeat("bb", 32)},
	})
	if e != nil {
		t.Fatal(e)
	}
	multi := filepath.Join(t.TempDir(), "multi.tar")
	f, e := os.Create(multi)
	if e != nil {
		t.Fatal(e)
	}
	tw := tar.NewWriter(f)
	if e = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0644, Size: int64(len(twoImages))}); e != nil {
		t.Fatal(e)
	}
	if _, e = tw.Write(twoImages); e != nil {
		t.Fatal(e)
	}
	if e = tw.Close(); e != nil {
		t.Fatal(e)
	}
	if e = f.Close(); e != nil {
		t.Fatal(e)
	}
	if _, e := savedConfigID(multi); e == nil || !strings.Contains(e.Error(), "exactly one") {
		t.Fatalf("multi-image archive must fail closed, got %v", e)
	}
	_ = config
}

func TestLoadReportRefsParsesDaemonOutput(t *testing.T) {
	report := "Loaded image: edge-delta-demo-abc:app-a-v1\nLoaded image ID: sha256:" + strings.Repeat("ab", 32) + "\n"
	refs := loadReportRefs(report)
	if len(refs) != 2 || refs[0] != "edge-delta-demo-abc:app-a-v1" || refs[1] != "sha256:"+strings.Repeat("ab", 32) {
		t.Fatalf("refs = %v", refs)
	}
	if refs := loadReportRefs(""); len(refs) != 0 {
		t.Fatalf("empty report must name no refs, got %v", refs)
	}
	if refs := loadReportRefs("Loaded image ID: sha256:zz\n"); len(refs) != 0 {
		t.Fatalf("non-hex ID must be rejected, got %v", refs)
	}
}

func TestVerifyLoadedFailsClosedWithoutDaemonReport(t *testing.T) {
	if e := verifyLoadedConfigIdentity(context.Background(), "", []string{"sha256:" + strings.Repeat("0", 64)}); e == nil {
		t.Fatal("empty load report must fail closed")
	}
	if _, e := savedConfigID(""); e == nil {
		t.Fatal("unsaveable reference must fail closed")
	}
	_ = fmt.Sprint
}
