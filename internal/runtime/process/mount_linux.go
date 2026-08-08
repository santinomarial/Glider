//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// privatizeMountTree breaks mount propagation to/from the host before any
// other mount in this namespace is created (runtime.md §4 step 1). It must
// run first: everything that follows depends on nothing here being able to
// leak back to the host's mount table.
func privatizeMountTree() error {
	if err := syscall.Mount("", "/", "", syscall.MS_REC|syscall.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("privatize mount tree: %w", err)
	}
	return nil
}

// bindMountSelf makes root a mount point (bind-mounted onto itself), which
// pivot_root requires of its target (runtime.md §4 step 2).
func bindMountSelf(root string) error {
	if err := syscall.Mount(root, root, "", syscall.MS_BIND|syscall.MS_REC, ""); err != nil {
		return fmt.Errorf("bind-mount rootfs onto itself: %w", err)
	}
	return nil
}

// setupRootMounts mounts /proc, /sys, /dev, /dev/pts, and /dev/shm under
// root, before pivot_root switches root to be "/" (runtime.md §4 step 4).
// Mounting them here — targeting <root>/proc rather than /proc — is
// equivalent to mounting them after pivot_root at /proc: pivot_root only
// changes which mount is visible at "/", it doesn't move these submounts.
// Doing it here keeps pivot_root itself a minimal, late, low-risk final
// step (runtime.md §3's sync diagram: "configure mounts" before the ready
// signal, "pivot_root, execve" only after "go").
func setupRootMounts(root string) error {
	if err := mountProc(root); err != nil {
		return err
	}
	if err := mountSysReadOnly(root); err != nil {
		return err
	}
	if err := mountDev(root); err != nil {
		return err
	}
	if err := mountDevPts(root); err != nil {
		return err
	}
	if err := mountDevShm(root); err != nil {
		return err
	}
	if err := mountRuntimeSelf(root); err != nil {
		return err
	}
	return nil
}

const workloadRuntimePath = "/dev/.glider-runtime"

// mountRuntimeSelf places a read-only bind of the current executable on the
// container-private /dev tmpfs. After pivot_root, the original host path
// behind /proc/self/exe is unreachable, but the security trampoline still
// needs one controlled re-exec before it execs the workload.
func mountRuntimeSelf(root string) error {
	target := filepath.Join(root, workloadRuntimePath)
	source, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve runtime executable: %w", err)
	}
	if source, err = filepath.EvalSymlinks(source); err != nil {
		return fmt.Errorf("resolve runtime executable symlinks: %w", err)
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_RDONLY, 0o500)
	if err != nil {
		return fmt.Errorf("create runtime trampoline mountpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := syscall.Mount(source, target, "", syscall.MS_BIND, ""); err != nil {
		return fmt.Errorf("bind runtime trampoline: %w", err)
	}
	if err := syscall.Mount("", target, "", syscall.MS_BIND|syscall.MS_REMOUNT|syscall.MS_RDONLY|syscall.MS_NOSUID|syscall.MS_NODEV, ""); err != nil {
		return fmt.Errorf("secure runtime trampoline mount: %w", err)
	}
	return nil
}

func mountProc(root string) error {
	target := filepath.Join(root, "proc")
	if err := os.MkdirAll(target, 0o555); err != nil {
		return fmt.Errorf("create /proc mountpoint: %w", err)
	}
	// Reflects this process's own PID namespace (already active via the
	// clone flags used to start this process) regardless of mount order
	// relative to pivot_root (runtime.md §4 step 4 note).
	if err := syscall.Mount("proc", target, "proc", 0, ""); err != nil {
		return fmt.Errorf("mount /proc: %w", err)
	}
	return nil
}

func mountSysReadOnly(root string) error {
	target := filepath.Join(root, "sys")
	if err := os.MkdirAll(target, 0o555); err != nil {
		return fmt.Errorf("create /sys mountpoint: %w", err)
	}
	if err := syscall.Mount("sysfs", target, "sysfs", 0, ""); err != nil {
		return fmt.Errorf("mount /sys: %w", err)
	}
	// A fresh (non-bind) mount accepts a plain MS_REMOUNT to change its
	// flags; read-only is the Phase 1 default per runtime.md §4 step 4 —
	// a real access-control policy for /sys is security-model.md/Phase 8.
	if err := syscall.Mount("", target, "sysfs", syscall.MS_REMOUNT|syscall.MS_RDONLY, ""); err != nil {
		return fmt.Errorf("remount /sys read-only: %w", err)
	}
	return nil
}

// devNodes are bind-mounted individually from the host rather than created
// with mknod: it avoids needing CAP_MKNOD semantics to line up per node and
// is the same approach used by minimal container runtimes for this exact
// step (runtime.md §4 step 4: "a minimal tmpfs with standard nodes, not a
// bind-mount of the host's /dev" — the tmpfs itself is fresh; only the
// individual device nodes placed into it are bind-mounted from the host).
var devNodes = []string{"null", "zero", "full", "random", "urandom", "tty"}

func mountDev(root string) error {
	target := filepath.Join(root, "dev")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create /dev mountpoint: %w", err)
	}
	if err := syscall.Mount("tmpfs", target, "tmpfs", syscall.MS_NOSUID|syscall.MS_STRICTATIME, "mode=755,size=65536k"); err != nil {
		return fmt.Errorf("mount /dev tmpfs: %w", err)
	}

	for _, name := range devNodes {
		dst := filepath.Join(target, name)
		f, err := os.OpenFile(dst, os.O_CREATE, 0o644)
		if err != nil {
			return fmt.Errorf("create /dev/%s bind target: %w", name, err)
		}
		f.Close()

		src := filepath.Join("/dev", name)
		if err := syscall.Mount(src, dst, "", syscall.MS_BIND, ""); err != nil {
			return fmt.Errorf("bind-mount /dev/%s: %w", name, err)
		}
	}

	if err := os.Symlink("pts/ptmx", filepath.Join(target, "ptmx")); err != nil {
		return fmt.Errorf("symlink /dev/ptmx: %w", err)
	}

	return nil
}

func mountDevPts(root string) error {
	target := filepath.Join(root, "dev", "pts")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create /dev/pts mountpoint: %w", err)
	}
	opts := "newinstance,ptmxmode=0666,mode=0620"
	if err := syscall.Mount("devpts", target, "devpts", 0, opts); err != nil {
		return fmt.Errorf("mount /dev/pts: %w", err)
	}
	return nil
}

func mountDevShm(root string) error {
	target := filepath.Join(root, "dev", "shm")
	if err := os.MkdirAll(target, 0o755); err != nil {
		return fmt.Errorf("create /dev/shm mountpoint: %w", err)
	}
	flags := uintptr(syscall.MS_NOSUID | syscall.MS_NODEV | syscall.MS_STRICTATIME)
	if err := syscall.Mount("tmpfs", target, "tmpfs", flags, "mode=1777,size=65536k"); err != nil {
		return fmt.Errorf("mount /dev/shm: %w", err)
	}
	return nil
}
