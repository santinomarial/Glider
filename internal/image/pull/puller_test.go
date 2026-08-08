package pull

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	digest "github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/santinomarial/glider/internal/image/content"
	"github.com/santinomarial/glider/internal/image/registry"
)

func TestPullOCIManifestStoresConfigAndLayers(t *testing.T) {
	config := mustJSON(t, v1.Image{Platform: v1.Platform{OS: "linux", Architecture: "amd64"}, Config: v1.ImageConfig{Entrypoint: []string{"/bin/app"}, Env: []string{"A=B"}, WorkingDir: "/srv"}})
	layer := []byte("compressed-layer")
	configDesc := descriptor(config, v1.MediaTypeImageConfig)
	layerDesc := descriptor(layer, v1.MediaTypeImageLayerGzip)
	manifest := mustJSON(t, v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest, Config: configDesc, Layers: []v1.Descriptor{layerDesc}})
	manifestDigest := digest.FromBytes(manifest)
	var blobRequests atomic.Int32

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/team/app/manifests/v1":
			w.Header().Set("Content-Type", v1.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDigest.String())
			_, _ = w.Write(manifest)
		case "/v2/team/app/blobs/" + configDesc.Digest.String():
			blobRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(config)))
			_, _ = w.Write(config)
		case "/v2/team/app/blobs/" + layerDesc.Digest.String():
			blobRequests.Add(1)
			w.Header().Set("Content-Length", fmt.Sprint(len(layer)))
			_, _ = w.Write(layer)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	store, err := content.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	puller, _ := New(registry.NewClient(server.Client(), nil, true), store)
	input := strings.TrimPrefix(server.URL, "http://") + "/team/app:v1"
	result, err := puller.Pull(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Digest != manifestDigest || result.Image.Config.WorkingDir != "/srv" || result.Image.Config.Entrypoint[0] != "/bin/app" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if err := store.Verify(configDesc); err != nil {
		t.Fatal(err)
	}
	if err := store.Verify(layerDesc); err != nil {
		t.Fatal(err)
	}

	if _, err := puller.Pull(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if got := blobRequests.Load(); got != 2 {
		t.Fatalf("cached pull made extra blob requests: total %d, want 2", got)
	}
}

func TestPullSelectsLinuxAMD64FromIndex(t *testing.T) {
	config := mustJSON(t, v1.Image{Platform: v1.Platform{OS: "linux", Architecture: "amd64"}})
	configDesc := descriptor(config, v1.MediaTypeImageConfig)
	manifest := mustJSON(t, v1.Manifest{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageManifest, Config: configDesc})
	manifestDesc := descriptor(manifest, v1.MediaTypeImageManifest)
	manifestDesc.Platform = &v1.Platform{OS: "linux", Architecture: "amd64"}
	other := manifestDesc
	other.Digest = digest.FromString("other")
	other.Platform = &v1.Platform{OS: "linux", Architecture: "arm64"}
	index := mustJSON(t, v1.Index{Versioned: specs.Versioned{SchemaVersion: 2}, MediaType: v1.MediaTypeImageIndex, Manifests: []v1.Descriptor{other, manifestDesc}})
	indexDigest := digest.FromBytes(index)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v2/repo/app/manifests/latest":
			w.Header().Set("Content-Type", v1.MediaTypeImageIndex)
			w.Header().Set("Docker-Content-Digest", indexDigest.String())
			_, _ = w.Write(index)
		case "/v2/repo/app/manifests/" + manifestDesc.Digest.String():
			w.Header().Set("Content-Type", v1.MediaTypeImageManifest)
			w.Header().Set("Docker-Content-Digest", manifestDesc.Digest.String())
			_, _ = w.Write(manifest)
		case "/v2/repo/app/blobs/" + configDesc.Digest.String():
			w.Header().Set("Content-Length", fmt.Sprint(len(config)))
			_, _ = w.Write(config)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	store, _ := content.NewStore(t.TempDir())
	puller, _ := New(registry.NewClient(server.Client(), nil, true), store)
	result, err := puller.Pull(context.Background(), strings.TrimPrefix(server.URL, "http://")+"/repo/app")
	if err != nil {
		t.Fatal(err)
	}
	if result.Manifest.Digest != manifestDesc.Digest {
		t.Fatalf("selected %s, want %s", result.Manifest.Digest, manifestDesc.Digest)
	}
}

func descriptor(data []byte, mediaType string) v1.Descriptor {
	return v1.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(data), Size: int64(len(data))}
}
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
