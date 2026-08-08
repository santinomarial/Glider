//go:build linux

package process

import (
	"fmt"
	"os"
	"time"

	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// cgroupCleanupTimeout bounds how long a cgroup cleanup waits for its
// cgroup to become unpopulated before giving up (Phase 4 §20's "bound all
// waits") — by the time every caller of this reaches it, the owning
// process(es) are already confirmed exited/reaped, so this is generous
// headroom for the kernel's own bookkeeping to catch up, not a real
// expected wait.
const cgroupCleanupTimeout = 5 * time.Second

// toStateResources converts cgroup.Resources into the durable,
// dependency-free state.Resources shadow persisted in the state record
// (state/state.go's Resources doc comment explains the split).
func toStateResources(r cgroup.Resources) state.Resources {
	return state.Resources{
		CPUCores:    r.CPUCores,
		MemoryBytes: r.MemoryBytes,
		PIDsMax:     r.PIDsMax,
	}
}

// cleanupContainerCgroup idempotently removes containerID's cgroup using
// an already-constructed Manager, waiting (bounded) for it to become
// unpopulated first. Best-effort: failures are reported to stderr, never
// escalated — a cgroup this couldn't remove is left for a later
// `process.Recover` pass (cleanup.go extends the same idempotent removal
// there), never treated as fatal to whatever launch/shutdown path is
// already in progress.
func cleanupContainerCgroup(mgr *cgroup.Manager, containerID string) {
	if err := mgr.WaitUnpopulated(containerID, cgroupCleanupTimeout); err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: container cgroup did not become unpopulated for cleanup: %v\n", err)
		return
	}
	if err := mgr.Remove(containerID); err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: could not remove container cgroup: %v\n", err)
	}
}

// cleanupContainerCgroupByName is cleanupContainerCgroup for callers that
// don't already have a Manager in scope (constructing one is cheap and
// side-effect-free — cgroup.NewManager only discovers the mount point, it
// does not delegate/bootstrap anything).
func cleanupContainerCgroupByName(containerID string) {
	mgr, err := cgroup.NewManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: could not discover cgroup v2 mount for cleanup: %v\n", err)
		return
	}
	cleanupContainerCgroup(mgr, containerID)
}
