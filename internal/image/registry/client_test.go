package registry

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	digest "github.com/opencontainers/go-digest"
)

func TestFetchManifestBearerChallenge(t *testing.T) {
	manifest := []byte(`{"schemaVersion":2}`)
	wantDigest := digest.FromBytes(manifest)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			if user, pass, ok := r.BasicAuth(); !ok || user != "u" || pass != "p" {
				t.Errorf("token request credentials = %q/%q/%v", user, pass, ok)
			}
			if r.URL.Query().Get("service") != "test" || r.URL.Query().Get("scope") != "repository:team/app:pull" {
				t.Errorf("token query = %s", r.URL.RawQuery)
			}
			fmt.Fprint(w, `{"token":"accepted"}`)
		case "/v2/team/app/manifests/latest":
			if r.Header.Get("Authorization") != "Bearer accepted" {
				w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer realm="%s/token",service="test",scope="repository:team/app:pull"`, server.URL))
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
			w.Header().Set("Docker-Content-Digest", wantDigest.String())
			_, _ = w.Write(manifest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	host := strings.TrimPrefix(server.URL, "http://")
	ref, err := referenceForTest(host + "/team/app:latest")
	if err != nil {
		t.Fatal(err)
	}
	client := NewClient(server.Client(), func(registry string) (Credentials, bool) { return Credentials{"u", "p"}, true }, true)
	data, desc, err := client.FetchManifest(context.Background(), ref, "latest")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(manifest) || desc.Digest != wantDigest {
		t.Fatalf("unexpected result: %s %s", data, desc.Digest)
	}
}

func TestFetchManifestRejectsAdvertisedDigestMismatch(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:"+strings.Repeat("a", 64))
		fmt.Fprint(w, `{}`)
	}))
	defer server.Close()
	ref, _ := referenceForTest(strings.TrimPrefix(server.URL, "http://") + "/repo:tag")
	_, _, err := NewClient(server.Client(), nil, true).FetchManifest(context.Background(), ref, "tag")
	if err == nil {
		t.Fatal("expected digest mismatch")
	}
}

func TestParseChallengeHandlesQuotedComma(t *testing.T) {
	scheme, values, err := parseChallenge(`Bearer realm="https://auth.example/token?a=b,c=d",service="registry.example",scope="repository:x/y:pull"`)
	if err != nil {
		t.Fatal(err)
	}
	if scheme != "Bearer" || values["realm"] != "https://auth.example/token?a=b,c=d" {
		t.Fatalf("unexpected parse: %q %#v", scheme, values)
	}
}
