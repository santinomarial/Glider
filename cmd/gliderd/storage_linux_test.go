//go:build linux

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/santinomarial/glider/internal/agent"
	"github.com/santinomarial/glider/internal/api"
)

type pressureStoreFake struct {
	node        api.Node
	event       api.Event
	eventCount  int
	evicted     bool
	onEvict     func()
	putConflict bool
}

func (f *pressureStoreFake) GetNode(context.Context, string) (api.Node, error) { return f.node, nil }
func (f *pressureStoreFake) PutNode(_ context.Context, node api.Node, expected int64) (api.Node, error) {
	if f.putConflict {
		f.putConflict = false
		f.node.Metadata.Revision++
		return node, errors.New("unexpected conflict placeholder")
	}
	if expected != f.node.Metadata.Revision {
		return node, errors.New("revision mismatch")
	}
	node.Metadata.Revision++
	f.node = node
	return node, nil
}
func (f *pressureStoreFake) EvictNodeAssignments(context.Context, string) error {
	f.evicted = true
	if f.onEvict != nil {
		f.onEvict()
	}
	return nil
}
func (f *pressureStoreFake) PutEvent(_ context.Context, event api.Event) (api.Event, error) {
	f.event = event
	f.eventCount++
	return event, nil
}

func TestDiskPressureCordonsBeforeEviction(t *testing.T) {
	store := &pressureStoreFake{node: api.Node{Metadata: api.Metadata{ID: "node-a", Revision: 4}, Status: api.NodeStatus{Phase: api.NodeReady}}}
	if err := evacuateDiskPressure(context.Background(), "node-a", store, 1000, 10); err != nil {
		t.Fatal(err)
	}
	if !store.node.Spec.Unschedulable || store.node.Status.Phase != api.NodeDraining || !store.node.Status.StoragePressure || !store.evicted {
		t.Fatalf("node=%+v evicted=%v", store.node, store.evicted)
	}
	if store.event.Reason != "StoragePressureEviction" || store.event.Fields["available_bytes"] != uint64(10) {
		t.Fatalf("event=%+v", store.event)
	}
	if err := evacuateDiskPressure(context.Background(), "node-a", store, 1000, 9); err != nil {
		t.Fatal(err)
	}
	if store.eventCount != 1 {
		t.Fatalf("duplicate pressure events = %d", store.eventCount)
	}
}

func TestRealTmpfsPressureEvictionRestoresReserve(t *testing.T) {
	root := t.TempDir()
	if err := unix.Mount("tmpfs", root, "tmpfs", 0, "size=16m"); err != nil {
		t.Skipf("requires mount capability: %v", err)
	}
	t.Cleanup(func() { _ = unix.Unmount(root, unix.MNT_DETACH) })
	driver, err := agent.NewRuntimeDriver(root, "10.70.0.0/24", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = driver.ConfigureStorage(time.Hour, 8<<20, 0); err != nil {
		t.Fatal(err)
	}
	pressureFile := filepath.Join(root, "running-workload-data")
	file, err := os.OpenFile(pressureFile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = unix.Fallocate(int(file.Fd()), 0, 0, 12<<20); err != nil {
		file.Close()
		t.Fatal(err)
	}
	file.Close()
	total, available, pressured, err := driver.DiskUsage()
	if err != nil || !pressured {
		t.Fatalf("total=%d available=%d pressured=%v err=%v", total, available, pressured, err)
	}
	store := &pressureStoreFake{node: api.Node{Metadata: api.Metadata{ID: "node-a", Revision: 1}, Status: api.NodeStatus{Phase: api.NodeReady}}, onEvict: func() { _ = os.Remove(pressureFile) }}
	if err = evacuateDiskPressure(context.Background(), "node-a", store, total, available); err != nil {
		t.Fatal(err)
	}
	_, recovered, stillPressured, err := driver.DiskUsage()
	if err != nil || stillPressured || recovered < 8<<20 {
		t.Fatalf("recovered=%d pressured=%v err=%v", recovered, stillPressured, err)
	}
}
