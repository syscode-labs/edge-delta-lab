// Package hubclient provides verified TLS clients for agent-to-hub connections.
package hubclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// Config contains PEM file paths, never inline credentials. The zero value uses
// system roots without a client certificate. A CA file adds to system trust.
type Config struct {
	CA         string `yaml:"hub_ca,omitempty"`
	ClientCert string `yaml:"hub_client_cert,omitempty"`
	ClientKey  string `yaml:"hub_client_key,omitempty"`
}

// ValidateURL rejects plaintext when TLS material is configured. Callers still
// control whether plaintext is allowed at all. URL credentials are never allowed.
func (c Config) ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("endpoint must be an absolute URL without credentials")
	}
	if u.Scheme != "https" && u.Scheme != "wss" {
		if u.Scheme != "http" && u.Scheme != "ws" {
			return errors.New("unsupported endpoint scheme")
		}
		if c != (Config{}) {
			return errors.New("TLS configuration requires HTTPS or WSS")
		}
	}
	return nil
}

// TLSConfig loads and validates trust material. The key must be a regular file
// with no group/other permissions. Errors never include file contents or paths.
// Certificates are loaded once; create a new client after rotating them.
func (c Config) TLSConfig() (*tls.Config, error) {
	if (c.ClientCert == "") != (c.ClientKey == "") {
		return nil, errors.New("hub client certificate and key must be configured together")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if c.CA != "" {
		b, err := os.ReadFile(c.CA)
		if err != nil {
			return nil, errors.New("cannot read hub CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(b) {
			return nil, errors.New("hub CA contains no certificates")
		}
		cfg.RootCAs = roots
	}
	if c.ClientKey != "" {
		f, err := os.Open(c.ClientKey)
		if err != nil {
			return nil, errors.New("cannot open hub client key")
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0177 != 0 {
			return nil, errors.New("hub client key must be a private regular file (0600 or stricter)")
		}
		key, err := io.ReadAll(f)
		if err != nil {
			return nil, errors.New("cannot read hub client key")
		}
		cert, err := os.ReadFile(c.ClientCert)
		if err != nil {
			return nil, errors.New("cannot read hub client certificate")
		}
		pair, err := tls.X509KeyPair(cert, key)
		if err != nil {
			return nil, errors.New("invalid hub client certificate/key pair")
		}
		cfg.Certificates = []tls.Certificate{pair}
	}
	return cfg, nil
}

// New returns an owned HTTP client. Redirects are disabled, so neither client
// identity nor requests can be redirected to another origin or plaintext.
// Call CloseIdleConnections when finished. Request contexts control cancellation.
func New(c Config, timeout time.Duration) (*http.Client, error) {
	cfg, err := c.TLSConfig()
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = cfg
	return &http.Client{Transport: &transport{base: tr, config: c}, Timeout: timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("hub redirects disabled") }}, nil
}

type transport struct {
	base   *http.Transport
	config Config
}

func (t *transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := t.config.ValidateURL(r.URL.String()); err != nil {
		return nil, err
	}
	return t.base.RoundTrip(r)
}
func (t *transport) CloseIdleConnections() { t.base.CloseIdleConnections() }
