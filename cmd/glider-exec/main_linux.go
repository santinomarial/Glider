//go:build linux

package main

import (
	"errors"
	"flag"
	"fmt"
	"github.com/santinomarial/glider/internal/runtime/security"
	"golang.org/x/sys/unix"
	"os"
	"runtime"
	"strconv"
	"syscall"
)

func main() {
	pid := flag.Int("pid", 0, "target container init PID")
	flag.Parse()
	if *pid <= 0 || flag.NArg() == 0 {
		fatal(errors.New("--pid and a command are required"))
	}
	if err := enter(*pid); err != nil {
		fatal(err)
	}
}
func enter(pid int) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// The Go runtime is multi-threaded and initially shares fs_struct across
	// threads. A private copy is required before joining a mount namespace.
	if err := unix.Unshare(unix.CLONE_FS); err != nil {
		return fmt.Errorf("unshare fs context: %w", err)
	}
	root, err := unix.Open("/proc/"+strconv.Itoa(pid)+"/root", unix.O_PATH|unix.O_DIRECTORY, 0)
	if err != nil {
		return fmt.Errorf("open container root: %w", err)
	}
	defer unix.Close(root)
	names := []string{"ipc", "uts", "net", "mnt", "pid"}
	fds := make([]int, 0, len(names))
	// Open every namespace while host /proc still names the target by its
	// host PID. After joining its mount namespace, /proc exposes namespace
	// PIDs and that host PID path intentionally disappears.
	for _, name := range names {
		fd, err := unix.Open("/proc/"+strconv.Itoa(pid)+"/ns/"+name, unix.O_RDONLY|unix.O_CLOEXEC, 0)
		if err != nil {
			for _, opened := range fds {
				unix.Close(opened)
			}
			return fmt.Errorf("open %s namespace: %w", name, err)
		}
		fds = append(fds, fd)
	}
	defer func() {
		for _, fd := range fds {
			unix.Close(fd)
		}
	}()
	for i, name := range names {
		err = unix.Setns(fds[i], 0)
		if err != nil {
			return fmt.Errorf("enter %s namespace: %w", name, err)
		}
	}
	if err := unix.Fchdir(root); err != nil {
		return fmt.Errorf("change to container root: %w", err)
	}
	if err := unix.Chroot("."); err != nil {
		return fmt.Errorf("chroot container root: %w", err)
	}
	if err := unix.Chdir("/"); err != nil {
		return fmt.Errorf("chdir after chroot: %w", err)
	}
	if err := security.ApplyDefault(); err != nil {
		return fmt.Errorf("apply exec security: %w", err)
	}
	argv := flag.Args()
	if _, err := os.Stat(argv[0]); err != nil {
		return fmt.Errorf("resolve command %s inside container: %w", argv[0], err)
	}
	pidChild, err := syscall.ForkExec(argv[0], argv, &syscall.ProcAttr{Dir: "/", Env: []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "TERM=" + os.Getenv("TERM")}, Files: []uintptr{os.Stdin.Fd(), os.Stdout.Fd(), os.Stderr.Fd()}})
	if err != nil {
		return fmt.Errorf("fork/exec %s: %w", argv[0], err)
	}
	var status syscall.WaitStatus
	for {
		_, err = syscall.Wait4(pidChild, &status, 0, nil)
		if err == syscall.EINTR {
			continue
		}
		if err != nil {
			return err
		}
		break
	}
	if status.Exited() {
		os.Exit(status.ExitStatus())
	}
	if status.Signaled() {
		os.Exit(128 + int(status.Signal()))
	}
	return nil
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "glider-exec:", err); os.Exit(126) }
