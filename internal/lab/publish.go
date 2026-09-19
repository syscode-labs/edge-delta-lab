package lab

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

type PublishOptions struct {
	Input, Root, Key, Release, Kind, Source string
	Sequence                                uint64
	ImageIDs                                []string
	Min, Avg, Max                           int
}

func Publish(o PublishOptions) (Manifest, error) {
	m := Manifest{Version: FormatVersion, Release: o.Release, Sequence: o.Sequence, Kind: o.Kind, Source: o.Source, ImageIDs: o.ImageIDs, Chunker: "gear64-lab-v1", Min: o.Min, Avg: o.Avg, Max: o.Max}
	if !safeRelease.MatchString(o.Release) || o.Sequence == 0 {
		return m, fmt.Errorf("invalid release or sequence")
	}
	key, e := ReadKey(o.Key, ed25519.PrivateKeySize)
	if e != nil {
		return m, e
	}
	f, e := os.Open(o.Input)
	if e != nil {
		return m, e
	}
	defer f.Close()
	h := sha256.New()
	e = Split(io.TeeReader(f, h), o.Min, o.Avg, o.Max, func(raw []byte) error {
		encoded, e := Encode(raw)
		if e != nil {
			return e
		}
		c := Chunk{SHA256: Hash(raw), Size: int64(len(raw)), EncodedSHA256: Hash(encoded), EncodedSize: int64(len(encoded))}
		path := filepath.Join(o.Root, BlobRel(c))
		// Encoding changes do not invalidate an edge's raw-content cache. A different
		// compressed representation gets a different immutable object key.
		existing, n, err := FileHash(path)
		if err != nil || existing != c.EncodedSHA256 || n != c.EncodedSize {
			if e = AtomicWrite(path, encoded, 0644); e != nil {
				return e
			}
		}
		m.Chunks = append(m.Chunks, c)
		m.ArtifactSize += c.Size
		return nil
	})
	if e != nil {
		return m, e
	}
	m.ArtifactSHA256 = hex.EncodeToString(h.Sum(nil))
	envelope, e := Sign(m, key)
	if e != nil {
		return m, e
	}
	if _, _, e = Verify(envelope, ed25519.PrivateKey(key).Public().(ed25519.PublicKey), 1<<50); e != nil {
		return m, e
	}
	path := filepath.Join(o.Root, "releases", o.Release+".json")
	if old, err := os.ReadFile(path); err == nil && string(old) != string(envelope) {
		return m, fmt.Errorf("release name is immutable: %s", o.Release)
	}
	return m, AtomicWrite(path, envelope, 0644)
}
