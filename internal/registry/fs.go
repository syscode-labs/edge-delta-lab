package registry

import (
	"bytes"
	"fmt"
	"os"

	"github.com/google/go-containerregistry/pkg/authn"
	"example.com/edge-delta-lab/internal/lab"
)

// readFile is a small indirection so tests can shadow disk behavior if needed.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// atomicWrite delegates to the v2 durable-write primitive (fsync + rename +
// dir fsync). Watcher state, sequence counters and channel promotions all go
// through it.
func atomicWrite(path string, b []byte, mode os.FileMode) error { return lab.AtomicWrite(path, b, mode) }

// basicAuther adapts AuthConfig to go-containerregistry's authn.Authenticator.
type basicAuther struct{ a AuthConfig }

func (b *basicAuther) Authorization() (*authn.AuthConfig, error) {
	return &authn.AuthConfig{Username: b.a.Username, Password: b.a.Password}, nil
}

// nextSequenceFile reads and bumps the persisted monotonic sequence counter.
func nextSequenceFile(path string) (uint64, error) {
	b, err := os.ReadFile(path)
	seq := uint64(0)
	if err == nil {
		if _, err := fmt.Sscanf(string(bytes.TrimSpace(b)), "%d", &seq); err != nil {
			return 0, fmt.Errorf("sequence file %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return 0, err
	}
	seq++
	if err := atomicWrite(path, []byte(fmt.Sprintf("%d\n", seq)), 0o600); err != nil {
		return 0, err
	}
	return seq, nil
}
