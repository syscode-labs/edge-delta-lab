// Package lab implements a dependency-free reference transport, not a production OTA product.
package lab

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
)

const FormatVersion = 1
const MaxManifestBytes = 32 << 20
const MaxChunkBytes = 4 << 20

var safeRelease = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,100}$`)
var hexDigest = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Chunk struct {
	SHA256        string `json:"sha256"`
	Size          int64  `json:"size"`
	EncodedSHA256 string `json:"encoded_sha256"`
	EncodedSize   int64  `json:"encoded_size"`
}
type Manifest struct {
	Version        int      `json:"version"`
	Release        string   `json:"release"`
	Sequence       uint64   `json:"sequence"`
	Kind           string   `json:"kind"`
	ArtifactSHA256 string   `json:"artifact_sha256"`
	ArtifactSize   int64    `json:"artifact_size"`
	ImageIDs       []string `json:"image_ids,omitempty"`
	Source         string   `json:"source,omitempty"`
	Chunker        string   `json:"chunker"`
	Min            int      `json:"min_chunk_bytes"`
	Avg            int      `json:"target_chunk_bytes"`
	Max            int      `json:"max_chunk_bytes"`
	Chunks         []Chunk  `json:"chunks"`
}

// Base64 preserves the exact signed bytes independently of JSON formatting.
type Envelope struct {
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

func Hash(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func FileHash(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func Keygen(dir string) error {
	if e := EnsureDir(dir); e != nil {
		return e
	}
	if _, e := os.Stat(filepath.Join(dir, "publisher.key")); !os.IsNotExist(e) {
		return errors.New("refusing to overwrite a key")
	}
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	if e = AtomicWrite(filepath.Join(dir, "publisher.key"), []byte(hex.EncodeToString(priv)+"\n"), 0600); e != nil {
		return e
	}
	return AtomicWrite(filepath.Join(dir, "publisher.pub"), []byte(hex.EncodeToString(pub)+"\n"), 0644)
}
func ReadKey(path string, size int) ([]byte, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	d, e := hex.DecodeString(string(bytes.TrimSpace(b)))
	if e != nil {
		return nil, e
	}
	if len(d) != size {
		return nil, fmt.Errorf("key must contain %d bytes", size)
	}
	return d, nil
}
func Sign(m Manifest, key ed25519.PrivateKey) ([]byte, error) {
	b, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	return json.Marshal(Envelope{base64.StdEncoding.EncodeToString(b), base64.StdEncoding.EncodeToString(ed25519.Sign(key, b))})
}
func Verify(data []byte, pub ed25519.PublicKey, maxArtifact int64) (Manifest, string, error) {
	var env Envelope
	var m Manifest
	if len(data) > MaxManifestBytes {
		return m, "", errors.New("manifest exceeds limit")
	}
	if e := json.Unmarshal(data, &env); e != nil {
		return m, "", e
	}
	p, e := base64.StdEncoding.DecodeString(env.Payload)
	if e != nil {
		return m, "", e
	}
	sig, e := base64.StdEncoding.DecodeString(env.Signature)
	if e != nil {
		return m, "", e
	}
	if !ed25519.Verify(pub, p, sig) {
		return m, "", errors.New("invalid release signature")
	}
	if e = json.Unmarshal(p, &m); e != nil {
		return m, "", e
	}
	if m.Version != FormatVersion || m.Chunker != "gear64-lab-v1" || !safeRelease.MatchString(m.Release) || m.Sequence == 0 {
		return m, "", errors.New("unsupported or invalid release")
	}
	if m.Kind != "raw" && m.Kind != "docker-archive" {
		return m, "", errors.New("unsupported artifact kind")
	}
	if m.Min < 64 || m.Min > m.Avg || m.Avg > m.Max || m.Max > MaxChunkBytes || m.Avg&(m.Avg-1) != 0 {
		return m, "", errors.New("invalid chunk parameters")
	}
	if m.ArtifactSize <= 0 || m.ArtifactSize > maxArtifact || !hexDigest.MatchString(m.ArtifactSHA256) {
		return m, "", errors.New("invalid artifact size or hash")
	}
	seen := map[string]Chunk{}
	var n int64
	for _, c := range m.Chunks {
		if !hexDigest.MatchString(c.SHA256) || !hexDigest.MatchString(c.EncodedSHA256) || c.Size <= 0 || c.Size > int64(m.Max) || c.EncodedSize <= 0 || c.EncodedSize > int64(m.Max)+65536 {
			return m, "", errors.New("invalid chunk descriptor")
		}
		if old, ok := seen[c.SHA256]; ok && old != c {
			return m, "", errors.New("inconsistent repeated chunk descriptor")
		}
		seen[c.SHA256] = c
		n += c.Size
		if n > m.ArtifactSize {
			return m, "", errors.New("chunk sizes exceed artifact")
		}
	}
	if n != m.ArtifactSize {
		return m, "", errors.New("chunk sizes do not equal artifact size")
	}
	for _, id := range m.ImageIDs {
		if len(id) != 71 || id[:7] != "sha256:" || !hexDigest.MatchString(id[7:]) {
			return m, "", errors.New("invalid Docker image ID")
		}
	}
	return m, Hash(p), nil
}
func BlobRel(c Chunk) string { return "chunks/" + c.EncodedSHA256[:2] + "/" + c.EncodedSHA256 + ".gz" }
func CachePath(state string, c Chunk) string {
	return filepath.Join(state, "cache", c.SHA256[:2], c.SHA256)
}
func Encode(raw []byte) ([]byte, error) {
	var b bytes.Buffer
	z, e := gzip.NewWriterLevel(&b, gzip.BestSpeed)
	if e != nil {
		return nil, e
	}
	if _, e = z.Write(raw); e != nil {
		return nil, e
	}
	if e = z.Close(); e != nil {
		return nil, e
	}
	return b.Bytes(), nil
}
func Decode(encoded []byte, c Chunk) ([]byte, error) {
	if int64(len(encoded)) != c.EncodedSize || Hash(encoded) != c.EncodedSHA256 {
		return nil, errors.New("encoded chunk integrity failure")
	}
	z, e := gzip.NewReader(bytes.NewReader(encoded))
	if e != nil {
		return nil, e
	}
	defer z.Close()
	raw, e := io.ReadAll(io.LimitReader(z, c.Size+1))
	if e != nil {
		return nil, e
	}
	if int64(len(raw)) != c.Size || Hash(raw) != c.SHA256 {
		return nil, errors.New("raw chunk integrity failure")
	}
	return raw, nil
}
func Unique(m Manifest) []Chunk {
	out := []Chunk{}
	seen := map[string]bool{}
	for _, c := range m.Chunks {
		if !seen[c.SHA256] {
			seen[c.SHA256] = true
			out = append(out, c)
		}
	}
	return out
}
