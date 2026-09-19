// Package registry implements a Docker Registry v2 (distribution spec) client,
// a polling watcher with allowlist/ignorelist and digest-change detection, and
// the registry→archive→publish pipeline trigger.
package registry

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Registry API media types we accept when resolving a manifest digest.
const (
	mtManifestV2   = "application/vnd.docker.distribution.manifest.v2+json"
	mtManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mtOCIManifest  = "application/vnd.oci.image.manifest.v1+json"
	mtOCIIndex     = "application/vnd.oci.image.index.v1+json"
)

var manifestMediaTypes = []string{mtManifestV2, mtManifestList, mtOCIManifest, mtOCIIndex}

// Client is a minimal Docker Registry v2 client with bearer-token auth.
type Client struct {
	Base *url.URL
	HTTP *http.Client

	mu    sync.Mutex
	tok   map[string]tokenEntry
	auth  AuthConfig
	limit int // pagination page size; 0 = server default
}

// AuthConfig describes optional basic credentials for the token exchange.
type AuthConfig struct {
	Username string
	Password string
}

type tokenEntry struct {
	token     string
	expiresAt time.Time
}

// NewClient builds a client for the registry rooted at base.
func NewClient(base string, auth AuthConfig) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("registry url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("registry url must be http(s): %q", base)
	}
	return &Client{Base: u, HTTP: &http.Client{Timeout: 60 * time.Second}, tok: map[string]tokenEntry{}, auth: auth}, nil
}

// Ping performs the v2 base endpoint check and returns the bearer challenge
// when the registry answers 401 with a WWW-Authenticate header.
func (c *Client) Ping(ctx context.Context) (ch challenge, needToken bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Base.String()+"/v2/", nil)
	if err != nil {
		return challenge{}, false, err
	}
	if c.auth.Username != "" || c.auth.Password != "" {
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return challenge{}, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusOK:
		return challenge{}, false, nil
	case resp.StatusCode == http.StatusUnauthorized:
		ch, err := parseChallenge(resp.Header.Get("WWW-Authenticate"))
		if err != nil {
			return challenge{}, false, fmt.Errorf("token auth challenge: %w", err)
		}
		return ch, true, nil
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return challenge{}, false, fmt.Errorf("registry ping %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
}

type challenge struct {
	realm, service, scope string
}

// parseChallenge parses: Bearer realm="https://auth",service="reg",scope="repository:foo:pull"
func parseChallenge(h string) (challenge, error) {
	h = strings.TrimSpace(h)
	if h == "" {
		return challenge{}, errors.New("missing WWW-Authenticate header")
	}
	scheme, rest, ok := strings.Cut(h, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return challenge{}, fmt.Errorf("unsupported auth scheme %q", scheme)
	}
	var ch challenge
	for _, kv := range splitChallengeParams(rest) {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.ToLower(k) {
		case "realm":
			ch.realm = v
		case "service":
			ch.service = v
		case "scope":
			ch.scope = v
		}
	}
	if ch.realm == "" {
		return challenge{}, errors.New("bearer challenge without realm")
	}
	return ch, nil
}

// splitChallengeParams splits on commas not inside quotes.
func splitChallengeParams(s string) []string {
	var out []string
	var b strings.Builder
	inQ := false
	for _, r := range s {
		switch {
		case r == '"':
			inQ = !inQ
			b.WriteRune(r)
		case r == ',' && !inQ:
			out = append(out, b.String())
			b.Reset()
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// fetchToken performs the realm token exchange and returns the token.
func (c *Client) fetchToken(ctx context.Context, ch challenge, scope string) (string, time.Time, error) {
	u, err := url.Parse(ch.realm)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("token realm: %w", err)
	}
	q := u.Query()
	if ch.service != "" {
		q.Set("service", ch.service)
	}
	if scope != "" {
		q.Set("scope", scope)
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	if c.auth.Username != "" || c.auth.Password != "" {
		req.SetBasicAuth(c.auth.Username, c.auth.Password)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", time.Time{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", time.Time{}, fmt.Errorf("token endpoint %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var tr struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", time.Time{}, fmt.Errorf("token body: %w", err)
	}
	tok := tr.Token
	if tok == "" {
		tok = tr.AccessToken
	}
	if tok == "" {
		return "", time.Time{}, errors.New("token endpoint returned no token")
	}
	ttl := 60 * time.Second
	if tr.ExpiresIn > 0 {
		ttl = time.Duration(tr.ExpiresIn) * time.Second
	}
	// Refresh 30 seconds before nominal expiry.
	return tok, time.Now().Add(ttl - 30*time.Second), nil
}

// tokenFor returns a cached or fresh bearer token for the scope.
func (c *Client) tokenFor(ctx context.Context, ch challenge, scope string) (string, error) {
	c.mu.Lock()
	if t, ok := c.tok[scope]; ok && time.Now().Before(t.expiresAt) {
		c.mu.Unlock()
		return t.token, nil
	}
	c.mu.Unlock()
	tok, exp, err := c.fetchToken(ctx, challenge{realm: ch.realm, service: ch.service}, scope)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.tok[scope] = tokenEntry{token: tok, expiresAt: exp}
	c.mu.Unlock()
	return tok, nil
}

// do performs a request with bearer-auth retry: on 401 it parses the
// WWW-Authenticate challenge, fetches a token for the request's scope and
// retries once with the Authorization header.
func (c *Client) do(ctx context.Context, method, path string, headers map[string]string) (*http.Response, error) {
	u := c.Base.String() + path
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	chRaw := resp.Header.Get("WWW-Authenticate")
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	resp.Body.Close()
	ch, err := parseChallenge(chRaw)
	if err != nil {
		return nil, fmt.Errorf("token auth challenge: %w", err)
	}
	scope := scopeForPath(path)
	tok, err := c.tokenFor(ctx, ch, scope)
	if err != nil {
		return nil, err
	}
	req2, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req2.Header.Set(k, v)
	}
	req2.Header.Set("Authorization", "Bearer "+tok)
	return c.HTTP.Do(req2)
}

// scopeForPath derives repository:<name>:pull from /v2/<name>/tags/list or
// /v2/<name>/manifests/<ref>.
func scopeForPath(path string) string {
	rest := strings.TrimPrefix(path, "/v2/")
	var repo string
	for _, suffix := range []string{"/tags/list", "/manifests/"} {
		if i := strings.Index(rest, suffix); i >= 0 {
			repo = rest[:i]
			break
		}
	}
	if repo == "" {
		return ""
	}
	return "repository:" + repo + ":pull"
}

// Tags lists all tags for repo, following n/last pagination.
func (c *Client) Tags(ctx context.Context, repo string) ([]string, error) {
	var out []string
	last := ""
	for page := 0; ; page++ {
		if page > 10000 {
			return nil, errors.New("pagination did not terminate")
		}
		q := url.Values{}
		if c.limit > 0 {
			q.Set("n", fmt.Sprintf("%d", c.limit))
		}
		if last != "" {
			q.Set("last", last)
		}
		path := "/v2/" + repo + "/tags/list"
		if len(q) > 0 {
			path += "?" + q.Encode()
		}
		resp, err := c.do(ctx, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
			resp.Body.Close()
			return nil, fmt.Errorf("tags/list %s: %s", repo, resp.Status+" "+strings.TrimSpace(string(body)))
		}
		var tl struct {
			Tags []string `json:"tags"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&tl); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("tags/list decode: %w", err)
		}
		resp.Body.Close()
		out = append(out, tl.Tags...)
		if c.limit <= 0 || len(tl.Tags) < c.limit {
			return out, nil
		}
		last = tl.Tags[len(tl.Tags)-1]
	}
}

// ManifestDigest resolves repo:ref to its manifest digest, preferring the
// Docker-Content-Digest header and falling back to hashing the raw body.
// Returns the digest, raw manifest bytes and content type.
func (c *Client) ManifestDigest(ctx context.Context, repo, ref string) (string, []byte, string, error) {
	path := "/v2/" + repo + "/manifests/" + ref
	headers := map[string]string{"Accept": strings.Join(manifestMediaTypes, ", ")}
	resp, err := c.do(ctx, http.MethodGet, path, headers)
	if err != nil {
		return "", nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return "", nil, "", fmt.Errorf("manifest %s:%s %s", repo, ref, resp.Status+" "+strings.TrimSpace(string(body)))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", nil, "", err
	}
	digest := resp.Header.Get("Docker-Content-Digest")
	if digest == "" {
		sum := sha256.Sum256(body)
		digest = "sha256:" + hex.EncodeToString(sum[:])
	}
	return digest, body, resp.Header.Get("Content-Type"), nil
}

// Blob fetches a blob (layer or config) by digest, returning its bytes.
func (c *Client) Blob(ctx context.Context, repo, digest string) ([]byte, error) {
	resp, err := c.do(ctx, http.MethodGet, "/v2/"+repo+"/blobs/"+digest, nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("blob %s %s", digest, resp.Status+" "+strings.TrimSpace(string(body)))
	}
	return io.ReadAll(resp.Body)
}
