package lab

import (
	"archive/tar"
	"compress/gzip"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

type LayerInfo struct {
	SHA256    string `json:"sha256"`
	RawBytes  int64  `json:"raw_bytes"`
	GzipBytes int64  `json:"gzip_bytes"`
}
type FixtureInfo struct {
	File                string      `json:"file"`
	Tag                 string      `json:"tag"`
	ImageID             string      `json:"image_id"`
	Layers              []LayerInfo `json:"layers"`
	ArtifactBytes       int64       `json:"artifact_bytes"`
	ChangedPayloadBytes int64       `json:"changed_payload_bytes"`
}
type countWriter struct{ n int64 }

func (w *countWriter) Write(b []byte) (int, error) { w.n += int64(len(b)); return len(b), nil }

// A reproducible, high-entropy stream prevents an all-zero fixture from giving
// misleading compression results. It is test data, not a cryptographic PRNG.
type fixtureRNG struct{ x uint64 }

func (r *fixtureRNG) Read(b []byte) (int, error) {
	for i := 0; i < len(b); {
		r.x += 0x9e3779b97f4a7c15
		z := r.x
		z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
		z = (z ^ (z >> 27)) * 0x94d049bb133111eb
		z ^= z >> 31
		var v [8]byte
		binary.LittleEndian.PutUint64(v[:], z)
		n := copy(b[i:], v[:])
		i += n
	}
	return len(b), nil
}
func writeFixtureLayer(path, name string, size int64, seed uint64, patch, insert int64) error {
	f, e := os.Create(path)
	if e != nil {
		return e
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: size + insert, Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	rng := &fixtureRNG{x: seed}
	buf := make([]byte, 64<<10)
	var offset int64
	inserted := false
	for offset < size {
		n := int64(len(buf))
		if size-offset < n {
			n = size - offset
		}
		_, _ = rng.Read(buf[:n])
		start := size / 2
		for i := int64(0); i < n; i++ {
			if offset+i >= start && offset+i < start+patch {
				buf[i] ^= 0x5a
			}
		}
		if !inserted && insert > 0 && offset+n >= start {
			split := start - offset
			if _, e = tw.Write(buf[:split]); e != nil {
				return e
			}
			// Insert nonzero deterministic data so compression cannot hide the insertion.
			extra := make([]byte, insert)
			_, _ = (&fixtureRNG{x: 789}).Read(extra)
			if _, e = tw.Write(extra); e != nil {
				return e
			}
			if _, e = tw.Write(buf[split:n]); e != nil {
				return e
			}
			inserted = true
		} else {
			if _, e = tw.Write(buf[:n]); e != nil {
				return e
			}
		}
		offset += n
	}
	if e = tw.Close(); e != nil {
		return e
	}
	return f.Close()
}
func layerInfo(path string) (LayerInfo, error) {
	h, n, e := FileHash(path)
	if e != nil {
		return LayerInfo{}, e
	}
	f, e := os.Open(path)
	if e != nil {
		return LayerInfo{}, e
	}
	defer f.Close()
	cw := &countWriter{}
	z := gzip.NewWriter(cw)
	if _, e = io.Copy(z, f); e != nil {
		return LayerInfo{}, e
	}
	if e = z.Close(); e != nil {
		return LayerInfo{}, e
	}
	return LayerInfo{h, n, cw.n}, nil
}
func tarFile(tw *tar.Writer, name, path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	s, e := f.Stat()
	if e != nil {
		return e
	}
	if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: s.Size(), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	_, e = io.Copy(tw, f)
	return e
}
func tarBytes(tw *tar.Writer, name string, b []byte) error {
	if e := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(b)), Typeflag: tar.TypeReg}); e != nil {
		return e
	}
	_, e := tw.Write(b)
	return e
}
func makeFixtureArchive(out, tag string, layers []string) (FixtureInfo, error) {
	infos := []LayerInfo{}
	diffs := []string{}
	names := []string{}
	for _, p := range layers {
		in, e := layerInfo(p)
		if e != nil {
			return FixtureInfo{}, e
		}
		infos = append(infos, in)
		diffs = append(diffs, "sha256:"+in.SHA256)
		names = append(names, in.SHA256+"/layer.tar")
	}
	config := map[string]any{"architecture": runtime.GOARCH, "os": "linux", "rootfs": map[string]any{"type": "layers", "diff_ids": diffs}, "config": map[string]any{"Cmd": []string{"/fixture-not-runnable"}}}
	c, _ := json.Marshal(config)
	id := Hash(c)
	manifest, _ := json.Marshal([]map[string]any{{"Config": id + ".json", "RepoTags": []string{tag}, "Layers": names}})
	f, e := os.Create(out)
	if e != nil {
		return FixtureInfo{}, e
	}
	defer f.Close()
	tw := tar.NewWriter(f)
	for i, p := range layers {
		if e = tarFile(tw, names[i], p); e != nil {
			return FixtureInfo{}, e
		}
	}
	if e = tarBytes(tw, id+".json", c); e != nil {
		return FixtureInfo{}, e
	}
	if e = tarBytes(tw, "manifest.json", manifest); e != nil {
		return FixtureInfo{}, e
	}
	if e = tw.Close(); e != nil {
		return FixtureInfo{}, e
	}
	if e = f.Close(); e != nil {
		return FixtureInfo{}, e
	}
	st, e := os.Stat(out)
	if e != nil {
		return FixtureInfo{}, e
	}
	return FixtureInfo{File: out, Tag: tag, ImageID: "sha256:" + id, Layers: infos, ArtifactBytes: st.Size()}, nil
}
func Fixtures(dir string, sizeMiB, patchKiB int) (map[string]FixtureInfo, error) {
	if sizeMiB < 2 || sizeMiB > 8192 || patchKiB < 1 {
		return nil, fmt.Errorf("size-mib must be 2..8192 and patch-kib positive")
	}
	if e := EnsureDir(dir); e != nil {
		return nil, e
	}
	work, e := os.MkdirTemp(dir, ".layers-")
	if e != nil {
		return nil, e
	}
	defer os.RemoveAll(work)
	size := int64(sizeMiB) << 20
	baseSize := size * 3 / 5
	appSize := size - baseSize
	patch := int64(patchKiB) << 10
	if patch > appSize/4 {
		return nil, fmt.Errorf("patch too large for this fixture")
	}
	base := filepath.Join(work, "base.tar")
	if e = writeFixtureLayer(base, "shared.blob", baseSize, 1, 0, 0); e != nil {
		return nil, e
	}
	out := map[string]FixtureInfo{}
	variants := []struct {
		Name          string
		Seed          uint64
		Patch, Insert int64
	}{{"app-a-v1", 2, 0, 0}, {"app-b-v1", 3, 0, 0}, {"app-a-v2", 2, patch, 0}, {"app-a-v3", 2, patch, 4096}}
	for _, v := range variants {
		p := filepath.Join(work, v.Name+".tar")
		if e = writeFixtureLayer(p, "app.blob", appSize, v.Seed, v.Patch, v.Insert); e != nil {
			return nil, e
		}
		info, e := makeFixtureArchive(filepath.Join(dir, v.Name+".tar"), "edge-delta-lab/"+v.Name+":fixture", []string{base, p})
		if e != nil {
			return nil, e
		}
		info.ChangedPayloadBytes = v.Patch + v.Insert
		out[v.Name] = info
	}
	b, _ := json.MarshalIndent(out, "", "  ")
	return out, AtomicWrite(filepath.Join(dir, "fixture-info.json"), b, 0644)
}
