package registry

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	authchallenge "github.com/docker/distribution/registry/client/auth/challenge"
)

// tokenRealm validates the challenge before either client constructs credentials.
// Separate token hosts remain supported, but a registry configured with HTTPS
// must never delegate authentication to plaintext HTTP. Errors omit realm URLs
// because a malicious challenge can include secrets in its URL.
func (c *Client) tokenRealm(realm string) (*url.URL, error) {
	u, err := url.Parse(realm)
	if err != nil {
		return nil, fmt.Errorf("invalid token realm URL")
	}
	if c.Base.Scheme == "https" && u.Scheme != "https" {
		return nil, fmt.Errorf("HTTPS registry requires HTTPS token realm")
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, fmt.Errorf("token realm must be an absolute HTTP(S) URL without userinfo")
	}
	return u, nil
}

// exportTransport applies the discovery client's transport and redirect policy
// to every go-containerregistry request, including token exchanges and blobs.
// Redirects are handled here so the library cannot forward credentials across
// origins (including ports or subdomains) via its own http.Client defaults.
type exportTransport struct{ c *Client }

func (t exportTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.c.Base.Scheme == "https" && req.URL.Scheme != "https" {
		return nil, fmt.Errorf("HTTPS registry refuses non-HTTPS request")
	}
	resp, err := t.c.send(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		// Use the same parser as go-containerregistry, including multiple
		// challenges, so no challenge it accepts can bypass realm validation.
		for _, ch := range authchallenge.ResponseChallenges(resp) {
			if strings.EqualFold(ch.Scheme, "bearer") {
				if _, err := t.c.tokenRealm(ch.Parameters["realm"]); err != nil {
					resp.Body.Close()
					return nil, err
				}
			}
		}
	}
	return resp, nil
}
