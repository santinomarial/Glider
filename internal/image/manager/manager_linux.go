//go:build linux

// Package manager composes the Phase 5-7 image pipeline without hiding the
// ownership boundaries of registry, content, unpack, and snapshot packages.
package manager

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/santinomarial/glider/internal/image/content"
	"github.com/santinomarial/glider/internal/image/pull"
	"github.com/santinomarial/glider/internal/image/registry"
	"github.com/santinomarial/glider/internal/image/snapshot"
	"github.com/santinomarial/glider/internal/image/unpack"
)

type Manager struct {
	mu        sync.Mutex
	content   *content.Store
	puller    *pull.Puller
	unpacker  *unpack.Unpacker
	snapshots *snapshot.Manager
}
type GCResult struct {
	BlobsRemoved   int
	LayersRemoved  int
	BytesReclaimed int64
}
type Prepared struct {
	RootFS     string
	Image      v1.Image
	Manifest   v1.Descriptor
	SnapshotID string
}

func New(root string, client *http.Client, credentials registry.CredentialFunc, insecure bool) (*Manager, error) {
	store, err := content.NewStore(filepath.Join(root, "content"))
	if err != nil {
		return nil, err
	}
	puller, err := pull.New(registry.NewClient(client, credentials, insecure), store)
	if err != nil {
		return nil, err
	}
	unpacker, err := unpack.New(filepath.Join(root, "layers"), unpack.Limits{})
	if err != nil {
		return nil, err
	}
	snapshots, err := snapshot.NewManager(filepath.Join(root, "snapshots"))
	if err != nil {
		return nil, err
	}
	return &Manager{content: store, puller: puller, unpacker: unpacker, snapshots: snapshots}, nil
}

func (m *Manager) Prepare(ctx context.Context, imageRef, snapshotID string) (Prepared, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.prepare(ctx, imageRef, snapshotID)
}

func (m *Manager) prepare(ctx context.Context, imageRef, snapshotID string) (Prepared, error) {
	result, err := m.puller.Pull(ctx, imageRef)
	if err != nil {
		return Prepared{}, err
	}
	layers := make([]string, 0, len(result.Layers))
	for _, desc := range result.Layers {
		blob, err := m.content.BlobPath(desc.Digest)
		if err != nil {
			return Prepared{}, err
		}
		layer, err := m.unpacker.Unpack(ctx, desc, blob)
		if err != nil {
			return Prepared{}, fmt.Errorf("unpack layer %s: %w", desc.Digest, err)
		}
		layers = append(layers, layer)
	}
	snap, err := m.snapshots.Ensure(snapshotID, layers)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{RootFS: snap.Merged, Image: result.Image, Manifest: result.Manifest, SnapshotID: snapshotID}, nil
}

func (m *Manager) Remove(snapshotID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshots.Remove(snapshotID)
}

// Collect reclaims unreferenced image data. Prepare, Remove, and Collect are
// serialized so the reference snapshot used by the collector is stable.
func (m *Manager) Collect(ctx context.Context, grace time.Duration) (GCResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	activeLayers, err := m.snapshots.ActiveLowerDirs()
	if err != nil {
		return GCResult{}, err
	}
	layers, err := m.unpacker.Collect(ctx, activeLayers, grace)
	if err != nil {
		return GCResult{}, err
	}
	// Blobs are staging data after verified unpacking. The grace period protects
	// recent downloads; manager serialization protects the active pull path.
	blobs, err := m.content.Collect(ctx, nil, grace)
	if err != nil {
		return GCResult{}, err
	}
	return GCResult{BlobsRemoved: blobs.Removed, LayersRemoved: layers.Removed, BytesReclaimed: blobs.Bytes + layers.Bytes}, nil
}
