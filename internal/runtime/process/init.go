//go:build linux

package process

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// RunInit is the child-side entrypoint, invoked as
// `/proc/self/exe __glider_init__ -- <workload argv...>` by Run (launcher.go).
// It is not a supported user-facing command (runtime.md §6, master plan
// Phase 1 §6): it assumes a very specific invocation contract — fds 3/4/5
// open as the ready/go/result pipes (config.go), the env vars in config.go
// set, and it is already running inside the namespaces Run's clone flags
// created. Called any other way, it fails fast rather than attempting
// anything privileged.
//
// Unlike Phase 1, RunInit never execs into the workload and never returns
// on the success path by that route either: per docs/adr/0006, glider-init
// remains PID 1 for the container's entire life, forking the workload as a
// supervised child instead. RunInit's only return path is via os.Exit,
// once the workload has been supervised to completion (or setup failed).
func RunInit(argv []string) {
	rootfs := os.Getenv(envRootFS)
	hostname := os.Getenv(envHostname)
	stateRoot := os.Getenv(envStateDir)
	containerID := os.Getenv(envContainerID)
	workingDir := os.Getenv(envWorkingDir)
	var imageEnv []string
	if raw := os.Getenv(envImageEnv); raw != "" { decoded, err := base64.StdEncoding.DecodeString(raw); if err != nil || json.Unmarshal(decoded,&imageEnv)!=nil { fmt.Fprintln(os.Stderr,"glider-runtime: invalid internal image environment");os.Exit(2) } }

	if rootfs == "" || hostname == "" || stateRoot == "" || containerID == "" || len(argv) == 0 {
		// Fds may not be valid yet if invoked outside the real contract
		// (e.g. a user typing the internal arg by hand) — stderr is the
		// only channel we can trust here.
		fmt.Fprintln(os.Stderr, "glider-runtime: __glider_init__ is an internal entrypoint and must not be invoked directly")
		os.Exit(2)
	}
	stopGrace := defaultStopGrace
	if raw := os.Getenv(envStopGrace); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			stopGrace = d
		}
	}

	resultW := os.NewFile(uintptr(fdResultWrite), "result-write")
	if resultW == nil || !fdValid(fdResultWrite) {
		fmt.Fprintln(os.Stderr, "glider-runtime: __glider_init__ missing required result descriptor")
		os.Exit(2)
	}
	// From here on, any pre-RUNNING failure is reported through resultW
	// instead of stderr (stderr is the workload's, once it starts — see
	// launcher.go's Stdio wiring). Mark it close-on-exec so a *successful*
	// exec inside cmd.Start() below (Go's own internal fork+exec, not
	// glider-init's) can't accidentally leak it into the workload.
	if err := setCloseOnExec(fdResultWrite); err != nil {
		fail(resultW, fmt.Errorf("mark result descriptor close-on-exec: %w", err))
	}

	if !fdValid(fdReadyWrite) || !fdValid(fdGoRead) {
		fail(resultW, fmt.Errorf("missing required synchronization descriptors"))
	}
	readyW := os.NewFile(uintptr(fdReadyWrite), "ready-write")
	goR := os.NewFile(uintptr(fdGoRead), "go-read")

	if err := privatizeMountTree(); err != nil {
		fail(resultW, err)
	}
	if err := syscall.Sethostname([]byte(hostname)); err != nil {
		fail(resultW, fmt.Errorf("set hostname: %w", err))
	}
	if err := bindMountSelf(rootfs); err != nil {
		fail(resultW, err)
	}
	if err := setupRootMounts(rootfs); err != nil {
		fail(resultW, err)
	}

	// Setup complete: signal the launcher so it can record CREATED
	// (container-lifecycle.md: "after namespaces + mounts ... succeed,
	// before the workload starts"), then block for its "go" before the
	// point of no return (runtime.md §3).
	if _, err := readyW.Write([]byte{1}); err != nil {
		fail(resultW, fmt.Errorf("signal ready: %w", err))
	}
	readyW.Close()

	buf := make([]byte, 1)
	if _, err := goR.Read(buf); err != nil {
		// EOF here means the launcher died before sending "go" (its write
		// end closed when its process exited) — this is the exact Phase 1
		// "launcher dies after CREATED" scenario, and it is handled simply
		// by falling through to fail(): no host resources beyond this
		// namespace's own mounts exist yet, and those are reclaimed by the
		// kernel automatically the moment this process exits (its private
		// mount and PID namespaces have no other members). Recovery's job
		// for the resulting stuck CREATED state file is in recovery.go.
		fail(resultW, fmt.Errorf("wait for launcher go-ahead: %w", err))
	}
	goR.Close()

	// Open the state directory *before* pivot_root: afterward, this
	// process's entire path-resolution root is the container's own rootfs
	// (that is the whole point of pivot_root — runtime.md §4), so the
	// host-side state directory is no longer reachable by path at all. An
	// already-open directory fd remains valid and usable via the openat(2)
	// family regardless (state/fdops_linux.go) — this is how
	// persistExited below can still durably record EXITED after
	// glider-init has long since pivoted away from being able to see this
	// path directly.
	stateDir := state.Dir(stateRoot, containerID)
	stateDirF, err := os.Open(stateDir)
	if err != nil {
		fail(resultW, fmt.Errorf("open state directory before pivot_root: %w", err))
	}
	// CLOEXEC: this fd must never leak into the workload (Phase 2 §18 —
	// every descriptor needs explicit ownership; a container process must
	// not be handed a live handle onto its own host-side state directory).
	if err := setCloseOnExec(int(stateDirF.Fd())); err != nil {
		fail(resultW, fmt.Errorf("mark state directory descriptor close-on-exec: %w", err))
	}

	if err := pivotRoot(rootfs); err != nil {
		fail(resultW, err)
	}
	if err := applyWorkloadEnvironment(imageEnv); err != nil { fail(resultW,err) }
	if workingDir != "" { if !filepath.IsAbs(workingDir) { fail(resultW,fmt.Errorf("OCI working directory must be absolute: %q",workingDir)) }; if err:=os.Chdir(workingDir);err!=nil{fail(resultW,fmt.Errorf("enter OCI working directory: %w",err))} }

	path, err := exec.LookPath(argv[0])
	if err != nil {
		fail(resultW, fmt.Errorf("resolve workload command %q: %w", argv[0], err))
	}

	runSupervisor(path, argv, resultW, supervisorConfig{
		stopGrace: stopGrace,
		stateDirF: stateDirF,
	})
	// runSupervisor never returns (it always ends in os.Exit) — reaching
	// here is itself a bug.
	fmt.Fprintln(os.Stderr, "glider-runtime: internal error: runSupervisor returned")
	os.Exit(2)
}

func applyWorkloadEnvironment(entries []string) error { for _,entry:=range entries{key,value,ok:=strings.Cut(entry,"=");if !ok||key==""||strings.ContainsRune(key,'\x00')||strings.ContainsRune(value,'\x00'){return fmt.Errorf("invalid OCI environment entry %q",entry)};if err:=os.Setenv(key,value);err!=nil{return err}};return nil }

func workloadEnvironment() []string { env:=os.Environ();out:=env[:0];for _,entry:=range env{if !strings.HasPrefix(entry,"_GLIDER_"){out=append(out,entry)}};return out }

// fail reports err to the launcher over the result channel and terminates.
// It is the only exit path for setup failures once resultW is known to be
// valid — used for everything up through workload-start (cmd.Start()
// failure in runSupervisor also calls this), i.e. every failure for which
// container-lifecycle.md's FAILED state applies ("no user process ever
// executed").
func fail(resultW *os.File, err error) {
	// Best-effort: if the launcher is already gone (e.g. it died before
	// CREATED, see goR.Read above), this write may get EPIPE — ignored
	// deliberately, matching os/signal's non-stdio SIGPIPE handling: it
	// returns a normal error here, not a fatal signal, and there is no
	// reader left to report to regardless.
	_, _ = resultW.Write(encodeFail(err.Error()))
	_ = resultW.Close()
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

// supervisorConfig is runSupervisor's configuration, kept separate from
// the setup-phase locals above so its signature stays small and testable
// in isolation from RunInit's env/fd plumbing.
type supervisorConfig struct {
	stopGrace time.Duration
	// stateDirF is a directory fd opened on the container's state
	// directory *before* pivot_root (see the doc comment where it's
	// opened, in RunInit) — the only way glider-init can still durably
	// write to it afterward.
	stateDirF *os.File
}

// runSupervisor forks+execs path/argv as glider-init's child and
// supervises it as PID 1 for the remainder of the container's life
// (docs/adr/0006-glider-init-pid1-supervisor.md). It never returns.
func runSupervisor(path string, argv []string, resultW *os.File, cfg supervisorConfig) {
	// Install signal handling BEFORE starting the workload: if the
	// workload's own exit happened to race a handler installed only after
	// Start(), SIGCHLD could be missed entirely (signals of the same type
	// are not queued individually). Notify must be in place before any
	// child that could send SIGCHLD exists.
	sigCh := make(chan os.Signal, 32)
	notified := append(append([]os.Signal{}, forwardedSignals()...), syscall.SIGCHLD)
	signal.Notify(sigCh, notified...)

	execStatusR, execStatusW, err := os.Pipe()
	if err != nil {
		fail(resultW, fmt.Errorf("create workload exec-status pipe: %w", err))
	}
	cmd := &exec.Cmd{
		Path:   "/proc/self/exe",
		Args:   append([]string{"/proc/self/exe", ReexecWorkloadArg, path}, argv...),
		Env:    workloadEnvironment(),
		Stdin:  os.Stdin,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		// A new process group, led by the workload itself, gives
		// glider-init a single deterministic signal-delivery target for
		// the workload's whole tree (docs/adr/0006 §"Workload process
		// groups"): kill(-pgid, sig) reaches every member that hasn't
		// itself called setsid() to leave the group (a documented
		// limitation, not a security property — see runtime.md §5).
		SysProcAttr: &syscall.SysProcAttr{Setpgid: true},
		ExtraFiles:  []*os.File{execStatusW},
	}

	if err := cmd.Start(); err != nil {
		execStatusR.Close()
		execStatusW.Close()
		fail(resultW, fmt.Errorf("start workload %q: %w", argv[0], err))
	}
	execStatusW.Close()
	mainPID := cmd.Process.Pid
	execResult, readErr := os.ReadFile("/proc/self/fd/" + fmt.Sprint(execStatusR.Fd()))
	execStatusR.Close()
	if readErr != nil || len(execResult) == 0 || execResult[0] != 1 || len(execResult) > 1 {
		_ = syscall.Kill(-mainPID, syscall.SIGKILL)
		_, _ = cmd.Process.Wait()
		reason := string(execResult)
		if len(execResult) > 0 && execResult[0] == 1 {
			reason = string(execResult[1:])
		}
		if reason == "" {
			reason = fmt.Sprint(readErr)
		}
		fail(resultW, fmt.Errorf("secure workload exec failed: %s", reason))
	}

	// cmd.Start() returning nil is authoritative confirmation that execve
	// succeeded (Go's os/exec implements the same fork+CLOEXEC-error-pipe
	// pattern runtime.md §7 used directly, internally, for this child —
	// docs/adr/0006). Report RUNNING and close the channel; nothing more
	// is ever sent on it (the eventual EXITED transition is written
	// directly to the state file — see persistExited below).
	_, _ = resultW.Write(encodeRunning())
	_ = resultW.Close()

	mainStatus := superviseLoop(sigCh, mainPID, cfg.stopGrace)

	// Best-effort cleanup of any remaining process-group members, then a
	// bounded, non-blocking drain of whatever already exited. Anything
	// still alive after glider-init exits is force-killed by the kernel's
	// PID-namespace-init-exit behavior regardless (docs/adr/0006) — this
	// is a courtesy for prompt cleanup, not the safety mechanism.
	_ = syscall.Kill(-mainPID, syscall.SIGKILL)
	finalDrain()

	code := exitCodeFromWaitStatus(mainStatus)
	persistExited(cfg.stateDirF, code)
	os.Exit(code)
}

// superviseLoop is glider-init's PID 1 event loop: reap children as they
// exit (SIGCHLD), forward the documented signal policy (signals.go) to the
// workload's process group, and escalate an in-progress graceful shutdown
// to SIGKILL after stopGrace. It returns once the main workload (mainPID)
// has been observed to exit.
func superviseLoop(sigCh <-chan os.Signal, mainPID int, stopGrace time.Duration) syscall.WaitStatus {
	var (
		stopRequested bool
		killDeadline  <-chan time.Time
	)

	for {
		select {
		case sig := <-sigCh:
			s, ok := sig.(syscall.Signal)
			if !ok {
				continue
			}
			if s == syscall.SIGCHLD {
				if st, exited := reapExited(mainPID); exited {
					return st
				}
				continue
			}
			if isLifecycleSignal(s) {
				if !stopRequested {
					stopRequested = true
					// Forward the actual signal received (SIGTERM or
					// SIGINT), not a hardcoded one — preserves the
					// standard 128+signal exit-code convention
					// (exitCodeFromWaitStatus) so operators can tell which
					// stop signal actually reached the workload.
					_ = syscall.Kill(-mainPID, s)
					killDeadline = time.After(stopGrace)
				}
				continue
			}
			// Transparent forward (SIGHUP/SIGQUIT/SIGWINCH): no shutdown
			// implication, just relay to the workload's group.
			_ = syscall.Kill(-mainPID, s)

		case <-killDeadline:
			killDeadline = nil
			_ = syscall.Kill(-mainPID, syscall.SIGKILL)
			// Do not assume the workload is dead yet — wait for the
			// corresponding SIGCHLD/reap like any other exit.
		}
	}
}

// exitedWaitForRunningTimeout bounds how long persistExited waits for the
// launcher's own RUNNING write to land on disk before giving up. This is
// NOT test-style "sleep and hope" synchronization (Phase 2 §16's
// prohibition on that is about tests guessing when a barrier has been
// crossed) — it is a bounded wait for a genuinely fast, expected write by
// a cooperating process (the launcher, immediately after reading
// glider-init's own RUNNING confirmation — launcher.go), needed because
// glider-init's own supervision (and therefore a possible workload exit)
// can legitimately race ahead of the launcher's disk write for an
// extremely short-lived workload. Milliseconds in the overwhelmingly
// common case; bounded generously here only to tolerate real scheduling
// contention under load, not to paper over a design gap.
const exitedWaitForRunningTimeout = 5 * time.Second

// persistExited durably records the workload's outcome directly (docs/adr/0006:
// glider-init, not the launcher, owns this transition, since it has the
// only authoritative knowledge of the real exit code and does not depend on
// the launcher still being alive). Failures here are reported to stderr
// but do not change glider-init's own exit code: a launcher that is still
// alive falls back to its own recovery-style inference if it observes
// glider-init exit without the state file reaching EXITED (launcher.go).
func persistExited(stateDirF *os.File, code int) {
	if stateDirF == nil {
		fmt.Fprintln(os.Stderr, "glider-runtime: no state directory handle available to record exit")
		return
	}
	dirFD := int(stateDirF.Fd())

	deadline := time.Now().Add(exitedWaitForRunningTimeout)
	backoff := time.Millisecond
	for {
		rec, done := tryPersistExited(dirFD, code)
		if done {
			return
		}
		// rec.Phase was neither EXITED (already done) nor RUNNING/STOPPING
		// (safe to transition from) — most likely still CREATED, meaning
		// the launcher hasn't finished its own RUNNING write yet. The lock
		// was already released by tryPersistExited before returning, so
		// waiting here cannot deadlock against the launcher acquiring it.
		if time.Now().After(deadline) {
			fmt.Fprintf(os.Stderr, "glider-runtime: gave up waiting for RUNNING to be durably recorded before EXITED (state stuck at %q)\n", rec.Phase)
			return
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

// tryPersistExited makes one attempt: acquire the lock, inspect the
// current phase, and either persist EXITED (returning done=true) or bail
// out having released the lock (done=false) so the caller can wait and
// retry without holding it — critical, since holding this lock while
// waiting would deadlock against the launcher's own saveTransition call
// trying to acquire the very same lock to write RUNNING.
func tryPersistExited(dirFD int, code int) (rec state.Record, done bool) {
	lock, err := state.LockAt(dirFD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: could not acquire state lock to record exit: %v\n", err)
		return state.Record{}, true
	}
	defer lock.Unlock()

	rec, err = state.LoadAt(dirFD)
	if err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: could not load state to record exit: %v\n", err)
		return rec, true
	}
	if rec.Phase == state.Exited {
		// Already recorded by a concurrent recovery pass — nothing to do.
		return rec, true
	}
	if rec.Phase != state.Running && rec.Phase != state.Stopping {
		return rec, false
	}

	if err := applyTransition(&rec, state.Exited); err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: cannot record EXITED: %v\n", err)
		return rec, true
	}
	rec.ExitCode = &code
	rec.ExitedInferred = false
	if err := state.SaveAt(dirFD, rec); err != nil {
		fmt.Fprintf(os.Stderr, "glider-runtime: could not persist EXITED state: %v\n", err)
	}
	return rec, true
}
