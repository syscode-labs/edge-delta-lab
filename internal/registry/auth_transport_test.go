package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func authOperation(t *testing.T, c *Client, operation string) error {
	t.Helper()
	ctx := context.Background()
	switch operation {
	case "tags":
		_, err := c.Tags(ctx, "proj/app")
		return err
	case "manifest":
		_, _, _, err := c.ManifestDigest(ctx, "proj/app", "v1")
		return err
	default:
		img, err := remoteImage(ctx, c, PublishRequest{Repo: "proj/app", Tag: "v1"})
		if err != nil {
			return err
		}
		return exportDockerArchive(img, filepath.Join(t.TempDir(), "image.tar"))
	}
}

func TestHTTPSRejectsHTTPTokenRealm(t *testing.T) {
	for _, operation := range []string{"tags", "manifest", "export"} {
		t.Run(operation, func(t *testing.T) {
			var requests, credentials atomic.Int32
			token := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "" {
					credentials.Add(1)
				}
				fmt.Fprint(w, `{"token":"test-token"}`)
			}))
			defer token.Close()
			reg := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q,service="test"`, token.URL))
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer reg.Close()
			c, err := NewClient(reg.URL, AuthConfig{Username: "test-bot", Password: "synthetic-password"})
			if err != nil {
				t.Fatal(err)
			}
			c.HTTP = reg.Client()
			err = authOperation(t, c, operation)
			if err == nil || !strings.Contains(err.Error(), "HTTPS token realm") {
				t.Errorf("expected clear downgrade rejection, got %v", err)
			}
			if requests.Load() != 0 || credentials.Load() != 0 {
				t.Fatalf("HTTP token endpoint received %d requests / %d credentials", requests.Load(), credentials.Load())
			}
		})
	}
}

func TestAuthenticatedDiscoveryAndExport(t *testing.T) {
	for _, tls := range []bool{false, true} {
		for _, bearer := range []bool{false, true} {
			for _, operation := range []string{"tags", "manifest", "export"} {
				t.Run(fmt.Sprintf("tls=%t/bearer=%t/%s", tls, bearer, operation), func(t *testing.T) {
					f, _, _ := startServingRegistry(t, "proj/app", "v1")
					t.Cleanup(f.realm.Close)
					var tokenRequests atomic.Int32
					var reg *httptest.Server
					handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						user, pass, ok := r.BasicAuth()
						basicOK := ok && user == "test-bot" && pass == "synthetic-password"
						if r.URL.Path == "/token" {
							tokenRequests.Add(1)
							if !basicOK {
								http.Error(w, "bad credentials", 401)
								return
							}
							fmt.Fprint(w, `{"token":"test-token","expires_in":300}`)
							return
						}
						authorized := basicOK
						if bearer {
							authorized = r.Header.Get("Authorization") == "Bearer test-token"
						}
						if !authorized {
							challenge := `Basic realm="test"`
							if bearer {
								challenge = fmt.Sprintf(`Bearer realm=%q,service="test"`, reg.URL+"/token")
							}
							w.Header().Set("WWW-Authenticate", challenge)
							w.WriteHeader(401)
							return
						}
						f.srv.Config.Handler.ServeHTTP(w, r)
					})
					if tls {
						reg = httptest.NewTLSServer(handler)
					} else {
						reg = httptest.NewServer(handler)
					}
					defer reg.Close()
					c, err := NewClient(reg.URL, AuthConfig{Username: "test-bot", Password: "synthetic-password"})
					if err != nil {
						t.Fatal(err)
					}
					c.HTTP = reg.Client()
					if err := authOperation(t, c, operation); err != nil {
						t.Fatal(err)
					}
					if bearer && tokenRequests.Load() == 0 {
						t.Fatal("token exchange was not exercised")
					}
				})
			}
		}
	}
}

func TestExportRejectsCrossOriginTokenRedirect(t *testing.T) {
	var requests atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, `{"token":"test-token"}`)
	}))
	defer target.Close()
	var reg *httptest.Server
	reg = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			http.Redirect(w, r, target.URL, http.StatusFound)
			return
		}
		w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm=%q`, reg.URL+"/token"))
		w.WriteHeader(401)
	}))
	defer reg.Close()
	c, err := NewClient(reg.URL, AuthConfig{Username: "test-bot", Password: "synthetic-password"})
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP = reg.Client()
	err = authOperation(t, c, "export")
	if err == nil || !strings.Contains(err.Error(), "cross-origin registry redirect refused") {
		t.Fatalf("got %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("cross-origin endpoint received %d requests", requests.Load())
	}
}

func TestTokenRealmValidation(t *testing.T) {
	c, err := NewClient("https://registry.example", AuthConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, realm := range []string{"http://registry.example/token", "/token", "//registry.example/token", "ftp://registry.example/token", "https://user:password@registry.example/token", "https:///token"} {
		if _, err := c.tokenRealm(realm); err == nil {
			t.Errorf("accepted %q", realm)
		}
	}
	// An explicit secure token host is not a redirect and remains supported.
	if _, err := c.tokenRealm("https://auth.example/token"); err != nil {
		t.Fatal(err)
	}
}
