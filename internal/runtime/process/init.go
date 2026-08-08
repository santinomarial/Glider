//go:build linux

package process

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// RunInit is the child-side entrypoint, invoked as
// `/proc/self/exe __glider_init__ -- <workload argv...>` by Run (launcher.go).
// It is not a supported user-facing command (runtime.md §6, master plan
// Phase 1 §6): it assumes a very specific invocation contract — fds 3/4/5
// open as the ready/go/err pipes (config.go), _GLIDER_ROOTFS and
// _GLIDER_HOSTNAME set, and it is already running inside the namespaces
// Run's clone flags created. Called any other way, it fails fast rather
// than attempting anything privileged.
//
// It never returns on the success path: it ends by replacing its own
// process image via execve into the workload, which remains PID 1.
func RunInit(argv []string) {
	rootfs := os.Getenv(envRootFS)
	hostname := os.Getenv(envHostname)

	if rootfs == "" || hostname == "" || len(argv) == 0 {
		// Fds may not be valid yet if invoked outside the real contract
		// (e.g. a user typing the internal arg by hand) — stderr is the
		// only channel we can trust here.
		fmt.Fprintln(os.Stderr, "glider-runtime: __glider_init__ is an internal entrypoint and must not be invoked directly")
		os.Exit(2)
	}

	errW := os.NewFile(uintptr(fdErrWrite), "err-write")
	if errW == nil || !fdValid(fdErrWrite) {
		fmt.Fprintln(os.Stderr, "glider-runtime: __glider_init__ missing required error-reporting descriptor")
		os.Exit(2)
	}
	// From here on, any failure is reported through errW instead of stderr
	// (stderr is the workload's, once we exec into it — see launcher.go's
	// Stdio wiring — so setup errors must not rely on it). CLOEXEC means a
	// *successful* final exec below closes this fd for us automatically;
	// we only ever write to it on the failure paths.
	if err := setCloseOnExec(fdErrWrite); err != nil {
		fail(errW, fmt.Errorf("mark error descriptor close-on-exec: %w", err))
	}

	if !fdValid(fdReadyWrite) || !fdValid(fdGoRead) {
		fail(errW, fmt.Errorf("missing required synchronization descriptors"))
	}
	readyW := os.NewFile(uintptr(fdReadyWrite), "ready-write")
	goR := os.NewFile(uintptr(fdGoRead), "go-read")

	if err := privatizeMountTree(); err != nil {
		fail(errW, err)
	}
	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		fail(errW, fmt.Errorf("set hostname: %w", err))
	}
	if err := bindMountSelf(rootfs); err != nil {
		fail(errW, err)
	}
	if err := setupRootMounts(rootfs); err != nil {
		fail(errW, err)
	}

	// Setup complete: signal the launcher so it can record CREATED
	// (container-lifecycle.md: "after namespaces + mounts ... succeed,
	// before execve"), then block for its "go" before the point of no
	// return (runtime.md §3).
	if _, err := readyW.Write([]byte{1}); err != nil {
		fail(errW, fmt.Errorf("signal ready: %w", err))
	}
	readyW.Close()

	buf := make([]byte, 1)
	if _, err := goR.Read(buf); err != nil {
		fail(errW, fmt.Errorf("wait for launcher go-ahead: %w", err))
	}
	goR.Close()

	if err := pivotRoot(rootfs); err != nil {
		fail(errW, err)
	}

	path, err := exec.LookPath(argv[0])
	if err != nil {
		fail(errW, fmt.Errorf("resolve workload command %q: %w", argv[0], err))
	}

	// If Exec succeeds, this process image is replaced and nothing below
	// runs; errW's CLOEXEC flag closes it, which is how the launcher
	// distinguishes "succeeded" from "about to report an error".
	if err := syscall.Exec(path, argv, os.Environ()); err != nil {
		fail(errW, fmt.Errorf("exec workload %q: %w", path, err))
	}
}

// fail reports err to the launcher and terminates. It is the only exit path
// for setup failures once errW is known to be valid.
func fail(errW *os.File, err error) {
	msg := err.Error()
	if errW != nil {
		_, _ = errW.Write([]byte(msg))
		_ = errW.Close()
	}
	os.Exit(1)
}

func fdValid(fd int) bool {
	var stat syscall.Stat_t
	return syscall.Fstat(fd, &stat) == nil
}

func setCloseOnExec(fd int) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, uintptr(fd), syscall.F_SETFD, syscall.FD_CLOEXEC)
	if errno != 0 {
		return errno
	}
	return nil
}
