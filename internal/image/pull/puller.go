// Package pull coordinates registry resolution and verified local storage for
// OCI images. Layer unpacking is intentionally a Phase 6 responsibility.
package pull

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"sync"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/santinomarial/glider/internal/image/content"
	"github.com/santinomarial/glider/internal/image/reference"
	"github.com/santinomarial/glider/internal/image/registry"
)

var (
	ErrUnsupportedMediaType = errors.New("unsupported OCI media type")
	ErrNoPlatform           = errors.New("image index has no supported platform")
	ErrInvalidManifest      = errors.New("invalid OCI manifest")
)

type Result struct {
	Reference reference.Reference
	Manifest  v1.Descriptor
	Config    v1.Descriptor
	Layers    []v1.Descriptor
	Image     v1.Image
}

type Puller struct {
	registry *registry.Client
	store    *content.Store
	os       string
	arch     string
	variant  string
}

func New(registryClient *registry.Client, store *content.Store) (*Puller, error) {
	return NewForPlatform(registryClient, store, runtime.GOOS, runtime.GOARCH, "")
}
func NewForPlatform(registryClient *registry.Client, store *content.Store, operatingSystem, architecture, variant string) (*Puller, error) {
	if registryClient == nil || store == nil {
		return nil, errors.New("puller requires registry client and content store")
	}
	if operatingSystem != "linux" || (architecture != "amd64" && architecture != "arm64") {
		return nil, errors.New("puller supports linux/amd64 and linux/arm64 only")
	}
	return &Puller{registry: registryClient, store: store, os: operatingSystem, arch: architecture, variant: variant}, nil
}

// Pull resolves input, selects the configured node platform from an index, and
// ensures the manifest, config, and every compressed layer blob are present
// and digest-verified in the content store.
func (p *Puller) Pull(ctx context.Context, input string) (Result, error) {
	ref, err := reference.Parse(input)
	if err != nil {
		return Result{}, err
	}
	data, desc, err := p.registry.FetchManifest(ctx, ref, ref.Selector())
	if err != nil {
		return Result{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	if _, err := p.store.Put(ctx, desc, bytes.NewReader(data)); err != nil {
		return Result{}, fmt.Errorf("store resolved manifest: %w", err)
	}

	if isIndex(desc.MediaType) {
		var index v1.Index
		if err := json.Unmarshal(data, &index); err != nil {
			return Result{}, fmt.Errorf("%w: decode index: %v", ErrInvalidManifest, err)
		}
		chosen, ok := selectPlatform(index.Manifests, p.os, p.arch, p.variant)
		if !ok {
			return Result{}, ErrNoPlatform
		}
		data, desc, err = p.registry.FetchManifest(ctx, ref, chosen.Digest.String())
		if err != nil {
			return Result{}, fmt.Errorf("fetch %s/%s manifest: %w", p.os, p.arch, err)
		}
		if desc.Digest != chosen.Digest || desc.Size != chosen.Size {
			return Result{}, fmt.Errorf("%w: selected manifest descriptor mismatch", ErrInvalidManifest)
		}
		if _, err := p.store.Put(ctx, desc, bytes.NewReader(data)); err != nil {
			return Result{}, fmt.Errorf("store platform manifest: %w", err)
		}
	}
	if !isManifest(desc.MediaType) {
		return Result{}, fmt.Errorf("%w: %q", ErrUnsupportedMediaType, desc.MediaType)
	}

	var manifest v1.Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Result{}, fmt.Errorf("%w: decode manifest: %v", ErrInvalidManifest, err)
	}
	if err := validateManifest(manifest); err != nil {
		return Result{}, err
	}
	descriptors := append([]v1.Descriptor{manifest.Config}, manifest.Layers...)
	if err := p.ensureBlobs(ctx, ref, descriptors, 4); err != nil {
		return Result{}, err
	}

	configPath, err := p.store.BlobPath(manifest.Config.Digest)
	if err != nil {
		return Result{}, err
	}
	configData, err := os.ReadFile(configPath)
	if err != nil {
		return Result{}, fmt.Errorf("read image config: %w", err)
	}
	var image v1.Image
	if err := json.Unmarshal(configData, &image); err != nil {
		return Result{}, fmt.Errorf("decode image config: %w", err)
	}
	if image.OS != "" && image.OS != p.os {
		return Result{}, fmt.Errorf("image config OS %q does not match %s", image.OS, p.os)
	}
	if image.Architecture != "" && image.Architecture != p.arch {
		return Result{}, fmt.Errorf("image config architecture %q does not match %s", image.Architecture, p.arch)
	}
	return Result{Reference: ref, Manifest: desc, Config: manifest.Config, Layers: append([]v1.Descriptor(nil), manifest.Layers...), Image: image}, nil
}

func (p *Puller) ensureBlobs(ctx context.Context, ref reference.Reference, descriptors []v1.Descriptor, parallelism int) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan v1.Descriptor)
	var wg sync.WaitGroup
	var firstErr error
	var errOnce sync.Once
	worker := func() {
		defer wg.Done()
		for desc := range jobs {
			if p.store.Verify(desc) == nil {
				continue
			}
			if _, err := p.registry.FetchBlob(ctx, ref, desc, p.store); err != nil {
				errOnce.Do(func() { firstErr = fmt.Errorf("fetch blob %s: %w", desc.Digest, err); cancel() })
				return
			}
		}
	}
	if parallelism > len(descriptors) {
		parallelism = len(descriptors)
	}
	for i := 0; i < parallelism; i++ {
		wg.Add(1)
		go worker()
	}
send:
	for _, desc := range descriptors {
		select {
		case jobs <- desc:
		case <-ctx.Done():
			break send
		}
	}
	close(jobs)
	wg.Wait()
	if firstErr == nil && ctx.Err() != nil {
		return ctx.Err()
	}
	return firstErr
}

func validateManifest(m v1.Manifest) error {
	if m.Config.Digest == "" || m.Config.Size < 0 {
		return fmt.Errorf("%w: missing config descriptor", ErrInvalidManifest)
	}
	for i, layer := range m.Layers {
		if layer.Digest == "" || layer.Size < 0 {
			return fmt.Errorf("%w: invalid layer descriptor %d", ErrInvalidManifest, i)
		}
	}
	return nil
}

func selectPlatform(manifests []v1.Descriptor, operatingSystem, architecture, variant string) (v1.Descriptor, bool) {
	for _, desc := range manifests {
		if desc.Platform != nil && desc.Platform.OS == operatingSystem && desc.Platform.Architecture == architecture && (variant == "" || desc.Platform.Variant == variant) {
			return desc, true
		}
	}
	return v1.Descriptor{}, false
}

func isIndex(mt string) bool {
	return mt == v1.MediaTypeImageIndex || mt == "application/vnd.docker.distribution.manifest.list.v2+json"
}
func isManifest(mt string) bool {
	return mt == v1.MediaTypeImageManifest || mt == "application/vnd.docker.distribution.manifest.v2+json"
}
