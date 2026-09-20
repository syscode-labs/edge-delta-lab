package registry

import "testing"

func TestImageReferencePreservesConfiguredScheme(t *testing.T) {
	// A non-loopback hostname is essential: go-containerregistry already
	// treats localhost as insecure, which would hide the HTTP regression.
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			c, err := NewClient(scheme+"://registry:5000", AuthConfig{})
			if err != nil {
				t.Fatal(err)
			}
			ref, err := imageReference(c, PublishRequest{Repo: "team/app", Tag: "release"})
			if err != nil {
				t.Fatal(err)
			}
			if got := ref.Context().Registry.Scheme(); got != scheme {
				t.Errorf("registry scheme = %q, want %q", got, scheme)
			}
			if got := ref.Name(); got != "registry:5000/team/app:release" {
				t.Errorf("reference = %q, want registry:5000/team/app:release", got)
			}
		})
	}
}
