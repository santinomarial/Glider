//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// pivotRoot replaces the mount namespace's root with root, per runtime.md
// §4 step 3. chroot alone is explicitly rejected (master plan §11, runtime.md
// §4): it doesn't change the namespace's root mount, so a process could
// still escape via a retained fd or ".." traversal. pivot_root actually
// swaps the root mount, which is what filesystem isolation requires.
//
// Preconditions: root must already be a mount point (bindMountSelf) and
// privatizeMountTree must already have run, per the ordering fixed in
// init.go.
func pivotRoot(root string) error {
	oldRoot := filepath.Join(root, ".oldroot")
	if err := os.MkdirAll(oldRoot, 0o700); err != nil {
		return fmt.Errorf("create pivot_root old-root target: %w", err)
	}

	if err := syscall.PivotRoot(root, oldRoot); err != nil {
		return fmt.Errorf("pivot_root: %w", err)
	}

	// pivot_root does not change the calling process's cwd; without this,
	// relative-path lookups could still resolve against the stale old-root
	// working directory inode.
	if err := os.Chdir("/"); err != nil {
		return fmt.Errorf("chdir to new root: %w", err)
	}

	// The old root is now mounted at /.oldroot, still busy (it's still the
	// mount pivot_root left it as); MNT_DETACH lazily unmounts it — it
	// disappears from the namespace's view immediately, with the kernel
	// releasing the underlying mount once nothing still references it.
	if err := syscall.Unmount("/.oldroot", syscall.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount old root: %w", err)
	}
	if err := os.RemoveAll("/.oldroot"); err != nil {
		return fmt.Errorf("remove old-root directory: %w", err)
	}

	return nil
}
