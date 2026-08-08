//go:build linux

package cgroup

import (
	"os"
	"strconv"
	"testing"
	"time"
)

// TestManagerRealKernelSmoke is a quick, privileged, real-kernel exercise
// of the whole Manager lifecycle (delegate, create, attach, verify, stats,
// remove) — a fast sanity check separate from the full black-box
// integration suite in test/integration/runtime, run directly against
// this package during development.
func TestManagerRealKernelSmoke(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root for real cgroup v2 operations")
	}

	m, err := NewManager()
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.EnsureDelegated(); err != nil {
		t.Fatalf("EnsureDelegated: %v", err)
	}

	id := "0123456789abcdef"
	t.Cleanup(func() { _ = m.Remove(id) })

	path, err := m.Create(id, Resources{CPUCores: 0.5, MemoryBytes: 64 * 1024 * 1024, PIDsMax: 32})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("created cgroup at %s", path)

	if err := m.Attach(id, os.Getpid()); err != nil {
		t.Fatalf("Attach: %v", err)
	}
	// Move back out before the test ends, so this test process doesn't
	// leave itself attached to (and thus blocking removal of) a cgroup
	// this test is about to delete.
	t.Cleanup(func() {
		_ = os.WriteFile(m.root+"/"+bootstrapLeaf+"/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0o644)
	})

	ok, err := m.VerifyAttached(id, os.Getpid())
	if err != nil {
		t.Fatalf("VerifyAttached: %v", err)
	}
	if !ok {
		t.Errorf("VerifyAttached = false, want true")
	}

	stats, err := m.Stats(id)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	t.Logf("stats: %+v", stats)
	if stats.PIDs.Current < 1 {
		t.Errorf("PIDs.Current = %d, want >= 1 (this test process)", stats.PIDs.Current)
	}

	// Move back out so the cgroup is unpopulated and removable.
	if err := os.WriteFile(m.root+"/"+bootstrapLeaf+"/cgroup.procs", []byte(strconv.Itoa(os.Getpid())), 0o644); err != nil {
		t.Fatalf("move out of container cgroup: %v", err)
	}

	if err := m.WaitUnpopulated(id, 2*time.Second); err != nil {
		t.Fatalf("WaitUnpopulated: %v", err)
	}
	if err := m.Remove(id); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	// Idempotent: removing again must not error.
	if err := m.Remove(id); err != nil {
		t.Errorf("second Remove: %v, want nil (idempotent)", err)
	}
}
