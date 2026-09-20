package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"testing"
)

func TestTriggerUsesConfiguredBasicPassword(t *testing.T) {
	f, _, digest := startServingRegistry(t, "proj/app", "v1")
	backend, err := url.Parse(f.srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(backend)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "bot" || password != "secret" {
			w.Header().Set("WWW-Authenticate", `Basic realm="Registry"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	defer srv.Close()
	cfg, _ := testCfg(t, srv.URL, []RepoConfig{{Name: "proj/app"}})
	cfg.Username, cfg.PasswordEnv = "bot", "TEST_REGISTRY_PASSWORD"
	t.Setenv(cfg.PasswordEnv, "secret")
	if _, err := Trigger(context.Background(), TriggerOptions{Config: cfg}, PublishRequest{Repo: "proj/app", Tag: "v1", Digest: digest}); err != nil {
		t.Fatal(err)
	}
}

func TestClientBasicAuth(t *testing.T) {
	for _, tls := range []bool{false, true} {
		t.Run(fmt.Sprint("tls=", tls), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				user, pass, ok := r.BasicAuth()
				if !ok || user != "bot" || pass != "secret" {
					w.Header().Set("WWW-Authenticate", `Basic realm="Registry"`)
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch r.URL.Path {
				case "/v2/":
					w.WriteHeader(http.StatusOK)
				case "/v2/proj/app/tags/list":
					if r.URL.Query().Get("last") == "v1" {
						fmt.Fprint(w, `{"tags":[]}`)
					} else {
						fmt.Fprint(w, `{"tags":["v1"]}`)
					}
				case "/v2/proj/app/manifests/v1":
					w.Header().Set("Docker-Content-Digest", "sha256:"+pad64("abcd"))
					fmt.Fprint(w, `{"layers":[]}`)
				default:
					http.NotFound(w, r)
				}
			})
			var srv *httptest.Server
			if tls {
				srv = httptest.NewTLSServer(handler)
			} else {
				srv = httptest.NewServer(handler)
			}
			defer srv.Close()
			for _, password := range []string{"secret", "wrong", ""} {
				c, err := NewClient(srv.URL, AuthConfig{Username: "bot", Password: password})
				if err != nil {
					t.Fatal(err)
				}
				c.HTTP = srv.Client()
				c.limit = 1
				tags, err := c.Tags(context.Background(), "proj/app")
				if password != "secret" {
					if err == nil {
						t.Fatalf("accepted password %q", password)
					}
					continue
				}
				if err != nil || len(tags) != 1 || tags[0] != "v1" {
					t.Fatalf("tags=%v err=%v", tags, err)
				}
				if _, need, err := c.Ping(context.Background()); err != nil || need {
					t.Fatalf("ping: %v %v", need, err)
				}
				if _, _, _, err := c.ManifestDigest(context.Background(), "proj/app", "v1"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestClientDoesNotLeakCredentialsOnRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("cross-origin redirect followed, authorization=%q", r.Header.Get("Authorization"))
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	}))
	defer source.Close()
	c, err := NewClient(source.URL, AuthConfig{Username: "bot", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Tags(context.Background(), "app"); err == nil {
		t.Fatal("expected cross-port redirect rejection")
	}
	if _, _, err := c.Ping(context.Background()); err == nil {
		t.Fatal("expected ping redirect rejection")
	}
	if _, _, err := c.fetchToken(context.Background(), challenge{realm: source.URL}, "repository:app:pull"); err == nil {
		t.Fatal("expected token redirect rejection")
	}
}

func TestClientSameOriginRedirectKeepsBasicAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tags" {
			http.Redirect(w, r, "/tags", http.StatusTemporaryRedirect)
			return
		}
		user, pass, ok := r.BasicAuth()
		if !ok || user != "bot" || pass != "secret" {
			t.Error("missing Basic credentials on same-origin redirect")
		}
		fmt.Fprint(w, `{"tags":["v1"]}`)
	}))
	defer srv.Close()
	c, _ := NewClient(srv.URL, AuthConfig{Username: "bot", Password: "secret"})
	if _, err := c.Tags(context.Background(), "app"); err != nil {
		t.Fatal(err)
	}
}
