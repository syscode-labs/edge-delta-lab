package registry

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"

	"example.com/edge-delta-lab/internal/lab"
)

// notifyEvent is the injectable event callback shape shared with cmd wiring.
type NotifyEvent func(kind, detail string)

// Serialize in-process publishers so sequence allocation and promotion cannot race.
// As with the sequence file, the publication root requires a single process writer.
var publicationMu sync.Mutex

// TriggerOptions carries the publish pipeline inputs derived from Config.
type TriggerOptions struct {
	Config Config
	// Event receives lifecycle/error events (wired to notify.Dispatcher by cmd).
	Event NotifyEvent
	// Registry override for tests; nil = build from Config.RegistryURL.
	Client *Client
}

// Trigger fetches the image for req from the registry, exports it as a
// docker-archive tar, and runs it through the unchanged v2 publish pipeline
// (lab.Publish + channel promote). Returns the release name.
func Trigger(ctx context.Context, opts TriggerOptions, req PublishRequest) (string, error) {
	publicationMu.Lock()
	defer publicationMu.Unlock()
	cfg := opts.Config
	release, err := releaseName(cfg.Publish.ReleasePrefix, req)
	if err != nil {
		return "", err
	}
	source := fmt.Sprintf("registry://%s/%s@%s", cfg.RegistryURL, req.Repo, req.Digest)
	// A signed release is the durable publication checkpoint. If promotion or
	// watcher-state persistence failed, reuse these exact bytes and sequence.
	envelope, err := os.ReadFile(filepath.Join(cfg.Publish.Root, "releases", release+".json"))
	if err == nil {
		key, err := lab.ReadKey(cfg.Publish.Key, ed25519.PrivateKeySize)
		if err != nil {
			return "", err
		}
		m, _, err := lab.Verify(envelope, ed25519.PrivateKey(key).Public().(ed25519.PublicKey), 1<<50)
		if err != nil {
			return "", fmt.Errorf("existing release: %w", err)
		}
		if m.Release != release || m.Source != source || m.Kind != "docker-archive" {
			return "", fmt.Errorf("existing release identity mismatch: %s", release)
		}
		if err := promote(cfg, envelope); err != nil {
			return "", err
		}
		if opts.Event != nil {
			opts.Event("release-published", fmt.Sprintf("release=%s repo=%s tag=%s digest=%s sequence=%d", release, req.Repo, req.Tag, req.Digest, m.Sequence))
		}
		return release, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	c := opts.Client
	if c == nil {
		var err error
		c, err = NewClient(cfg.RegistryURL, AuthConfig{Username: cfg.Username, Password: os.Getenv(cfg.PasswordEnv)})
		if err != nil {
			return "", err
		}
	}
	if opts.Event != nil {
		opts.Event("release-detected", fmt.Sprintf("%s:%s@%s", req.Repo, req.Tag, req.Digest))
	}

	img, err := remoteImage(ctx, c, req)
	if err != nil {
		return "", err
	}

	dir, err := os.MkdirTemp("", "edgelab-export")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	tarPath := filepath.Join(dir, "image.tar")
	if err := exportDockerArchive(img, tarPath); err != nil {
		return "", err
	}

	seq, err := nextSequence(cfg.Publish.SequenceFile)
	if err != nil {
		return "", err
	}
	// Unchanged v2 publish path: chunk + sign via lab.Publish (same CDC
	// defaults as the `publish` subcommand). ImageIDs carry the fetched
	// image's config digest — the exact ID Docker will report after load —
	// so the SIGNED manifest itself states which image IDs are authorized.
	// Without this, watcher-published releases could stage but never satisfy
	// docker-load identity verification.
	imgIDHash, err := img.ConfigName()
	if err != nil {
		return "", fmt.Errorf("config digest %s: %w", release, err)
	}
	imgID := "sha256:" + imgIDHash.Hex
	if _, err := lab.Publish(lab.PublishOptions{
		Input:    tarPath,
		Root:     cfg.Publish.Root,
		Key:      cfg.Publish.Key,
		Release:  release,
		Sequence: seq,
		Kind:     "docker-archive",
		Source:   source,
		ImageIDs: []string{imgID},
		Min:      16 << 10,
		Avg:      64 << 10,
		Max:      256 << 10,
	}); err != nil {
		return "", fmt.Errorf("publish %s: %w", release, err)
	}

	// Promote to the channel the same way `edgelab promote` does.
	env, err := os.ReadFile(filepath.Join(cfg.Publish.Root, "releases", release+".json"))
	if err != nil {
		return "", err
	}
	if err := promote(cfg, env); err != nil {
		return "", err
	}
	if opts.Event != nil {
		opts.Event("release-published", fmt.Sprintf("release=%s repo=%s tag=%s digest=%s sequence=%d", release, req.Repo, req.Tag, req.Digest, seq))
	}
	return release, nil
}

func promote(cfg Config, envelope []byte) error {
	path := filepath.Join(cfg.Publish.Root, "releases", cfg.Publish.Channel+".json")
	current, err := os.ReadFile(path)
	if err == nil {
		if bytes.Equal(current, envelope) {
			return nil
		}
		key, err := lab.ReadKey(cfg.Publish.Key, ed25519.PrivateKeySize)
		if err != nil {
			return err
		}
		pub := ed25519.PrivateKey(key).Public().(ed25519.PublicKey)
		old, _, err := lab.Verify(current, pub, 1<<50)
		if err != nil {
			return fmt.Errorf("current channel: %w", err)
		}
		next, _, err := lab.Verify(envelope, pub, 1<<50)
		if err != nil {
			return err
		}
		// A retry of an earlier successful release must never roll the channel
		// backward after another tag has been published.
		if old.Sequence > next.Sequence {
			return nil
		}
		if old.Sequence == next.Sequence {
			return fmt.Errorf("channel sequence %d already used by a different release", next.Sequence)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return lab.AtomicWrite(path, envelope, 0o644)
}

// remoteImage resolves the image referenced by req using the v2 client's HTTP
// transport (auth included) via go-containerregistry.
func remoteImage(ctx context.Context, c *Client, req PublishRequest) (v1.Image, error) {
	ref, err := imageReference(c, req)
	if err != nil {
		return nil, fmt.Errorf("parse reference: %w", err)
	}
	var opts []remote.Option
	if c.auth.Username != "" || c.auth.Password != "" {
		opts = append(opts, remote.WithAuth(&basicAuther{c.auth}))
	}
	opts = append(opts, remote.WithContext(ctx), remote.WithTransport(exportTransport{c}))
	// remote handles authentication only after our transport validates realms.
	img, err := remote.Image(ref, opts...)
	if err != nil {
		return nil, fmt.Errorf("remote image: %w", err)
	}
	// Prove the fetched manifest matches the digest the watcher saw.
	dg, err := img.Digest()
	if err != nil {
		return nil, err
	}
	if req.Digest != "" && dg.String() != req.Digest {
		return nil, fmt.Errorf("digest drift: watcher saw %s, export fetched %s", req.Digest, dg.String())
	}
	return img, nil
}

func imageReference(c *Client, req PublishRequest) (name.Reference, error) {
	var opts []name.Option
	// References contain no URL scheme, so preserve explicitly configured HTTP.
	// Without this option, non-loopback registries default to HTTPS.
	if c.Base.Scheme == "http" {
		opts = append(opts, name.Insecure)
	}
	return name.ParseReference(fmt.Sprintf("%s/%s:%s", hostForRef(c), req.Repo, req.Tag), opts...)
}

func hostForRef(c *Client) string {
	return c.Base.Host
}

// exportDockerArchive writes img as a docker-archive (manifest.json + config
// + layer tars), reusing go-containerregistry's tarball writer.
func exportDockerArchive(img v1.Image, path string) error {
	w, err := os.Create(path)
	if err != nil {
		return err
	}
	defer w.Close()
	// docker-archive layout; tarball.Write produces a docker-loadable tar.
	return tarball.Write(nil, img, w)
}

// releaseName derives the immutable release name for a publish request.
func releaseName(prefix string, req PublishRequest) (string, error) {
	if _, err := v1.NewHash(req.Digest); err != nil {
		return "", fmt.Errorf("release digest: %w", err)
	}
	name := prefix + sanitize(req.Repo) + "-" + sanitize(req.Tag)
	if name == "" {
		return "", fmt.Errorf("empty release name for %v", req)
	}
	// Tag movement and sanitized-name collisions must not overwrite a release.
	// Keep the full identity hash within lab's 101-character name limit.
	identity := sha256.Sum256([]byte(req.Repo + "\x00" + req.Tag + "\x00" + req.Digest))
	if len(name) > 36 {
		name = name[:36]
	}
	return fmt.Sprintf("%s-%x", name, identity), nil
}

// sanitize keeps lab safeRelease-compatible characters.
func sanitize(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		default:
			return '-'
		}
	}, s)
	return strings.Trim(s, "-.")
}

// nextSequence reads and bumps the persisted monotonic sequence file.
func nextSequence(path string) (uint64, error) {
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
	if seq == 0 {
		return 0, fmt.Errorf("sequence exhausted")
	}
	if err := atomicWrite(path, []byte(fmt.Sprintf("%d\n", seq)), 0o600); err != nil {
		return 0, err
	}
	return seq, nil
}
