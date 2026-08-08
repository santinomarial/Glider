//go:build linux

// Package manager composes the Phase 5-7 image pipeline without hiding the
// ownership boundaries of registry, content, unpack, and snapshot packages.
package manager

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/santinomarial/glider/internal/image/content"
	"github.com/santinomarial/glider/internal/image/pull"
	"github.com/santinomarial/glider/internal/image/registry"
	"github.com/santinomarial/glider/internal/image/snapshot"
	"github.com/santinomarial/glider/internal/image/unpack"
)

type Manager struct { content *content.Store; puller *pull.Puller; unpacker *unpack.Unpacker; snapshots *snapshot.Manager }
type Prepared struct { RootFS string; Image v1.Image; Manifest v1.Descriptor; SnapshotID string }

func New(root string, client *http.Client, credentials registry.CredentialFunc, insecure bool) (*Manager,error) {
	store,err:=content.NewStore(filepath.Join(root,"content"));if err!=nil{return nil,err}
	puller,err:=pull.New(registry.NewClient(client,credentials,insecure),store);if err!=nil{return nil,err}
	unpacker,err:=unpack.New(filepath.Join(root,"layers"),unpack.Limits{});if err!=nil{return nil,err}
	snapshots,err:=snapshot.NewManager(filepath.Join(root,"snapshots"));if err!=nil{return nil,err}
	return &Manager{content:store,puller:puller,unpacker:unpacker,snapshots:snapshots},nil
}

func (m *Manager) Prepare(ctx context.Context, imageRef, snapshotID string)(Prepared,error){
	result,err:=m.puller.Pull(ctx,imageRef);if err!=nil{return Prepared{},err}
	layers:=make([]string,0,len(result.Layers))
	for _,desc:=range result.Layers{blob,err:=m.content.BlobPath(desc.Digest);if err!=nil{return Prepared{},err};layer,err:=m.unpacker.Unpack(ctx,desc,blob);if err!=nil{return Prepared{},fmt.Errorf("unpack layer %s: %w",desc.Digest,err)};layers=append(layers,layer)}
	snap,err:=m.snapshots.Ensure(snapshotID,layers);if err!=nil{return Prepared{},err}
	return Prepared{RootFS:snap.Merged,Image:result.Image,Manifest:result.Manifest,SnapshotID:snapshotID},nil
}

func (m *Manager) Remove(snapshotID string) error { return m.snapshots.Remove(snapshotID) }
