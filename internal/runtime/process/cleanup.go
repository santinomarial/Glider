//go:build linux

package process

import (
	"errors"
	"fmt"
	"path/filepath"
	"syscall"

	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// containerOwnedMounts are the glider-owned mount points created under a
// container's RootFS (mount_linux.go), listed in reverse creation order
// for unmount.
var containerOwnedMounts = []string{
	filepath.Join("dev", "shm"),
	filepath.Join("dev", "pts"),
	"dev",
	"sys",
	"proc",
}

// cleanupContainer idempotently tears down everything Glider itself
// created for rec, without ever touching the user-supplied RootFS
// directory itself (container-lifecycle.md §6, Phase 2 §22's ownership
// distinction: Glider-owned resources vs. user-provided ones). It
// tolerates every step already being done — "already gone" is success, not
// error (container-lifecycle.md §6) — since it must be safely re-runnable
// after a crash mid-cleanup (§4).
//
// Under the current architecture (docs/adr/0006, runtime.md §4) these
// mounts live inside the container's own private mount namespace
// (MS_PRIVATE was set before any other mount was created — mount_linux.go's
// privatizeMountTree), which the kernel destroys automatically once every
// process that was ever inside it — glider-init included — has exited. By
// the time a container reaches DELETING there is therefore normally
// nothing host-visible left to unmount; this function still attempts the
// unmounts defensively/idempotently rather than assuming that's always
// true, so it stays correct if a later phase (OverlayFS, Phase 5-7)
// introduces a mount that *is* host-visible from cleanup's vantage point.
func cleanupContainer(rec state.Record) error {
	if err := ensureInitTerminated(rec); err != nil {
		return fmt.Errorf("ensure init terminated: %w", err)
	}

	if rec.RootFS == "" {
		return nil
	}
	for _, rel := range containerOwnedMounts {
		target := filepath.Join(rec.RootFS, rel)
		if err := syscall.Unmount(target, syscall.MNT_DETACH); err != nil {
			if errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOENT) {
				// EINVAL: not a mount point — already unmounted, or (the
				// expected Phase 2 case, see doc comment) was never
				// visible from outside the container's own now-destroyed
				// private namespace to begin with. ENOENT: path gone.
				// Both are success, not error.
				continue
			}
			return fmt.Errorf("unmount %s: %w", target, err)
		}
	}
	return nil
}

// ensureInitTerminated defensively confirms glider-init is gone before
// cleanup proceeds (container-lifecycle.md §6: teardown requires the
// owning process gone first) and forces it if a caller somehow reaches
// cleanup for a container recovery would have classified as still healthy.
// This is a last-resort safety net, not the primary termination path:
// finishDeleting (recovery.go) only ever reaches here for records already
// classified EXITED/FAILED/DELETING, where InitPID identifies an
// already-dead process by construction.
func ensureInitTerminated(rec state.Record) error {
	id := ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime}
	if id.IsZero() {
		return nil
	}
	alive, err := ValidateProcessIdentity(id)
	if err != nil {
		return err
	}
	if !alive {
		return nil
	}
	_ = syscall.Kill(id.PID, syscall.SIGKILL)
	return nil
}
