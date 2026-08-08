//go:build linux

package process

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/runtime/namespace"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// requireCgroupV2 fails fast on hosts without the cgroup v2 unified
// hierarchy (ADR-0001), even though Phase 1/2 do not themselves write any
// cgroup controls (that's Phase 4, runtime.md §6 "out of scope"). The
// unified hierarchy's cgroup.controllers file only exists under v2/unified
// mode — it is absent under v1 and legacy "hybrid" setups, making it a
// reliable, cheap detection check (runtime.md §6 "test environment").
func requireCgroupV2() error {
	if _, err := os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return fmt.Errorf("cgroup v2 unified hierarchy not found at /sys/fs/cgroup (ADR-0001 requires cgroup v2): %w", err)
	}
	return nil
}

// SupervisorFailureError distinguishes a Glider runtime/supervisor failure
// (glider-init crashed or was killed after the workload was already
// RUNNING, before it could durably record the workload's real outcome)
// from an ordinary workload failure (a normal nonzero or signaled exit,
// returned as a plain exit code, not an error) and from a pre-RUNNING
// setup FAILED (returned as a plain error, exit 1 — unchanged from Phase
// 1). runtime.md §7 requires this distinction stay visible rather than
// collapsing every error into exit 1; cmd/glider-runtime maps this
// specific case to its own distinct process exit code.
type SupervisorFailureError struct {
	Err error
}

func (e *SupervisorFailureError) Error() string { return e.Err.Error() }
func (e *SupervisorFailureError) Unwrap() error { return e.Err }

// Run launches a single container per cfg and blocks until it exits,
// following the parent-owned lifecycle in container-lifecycle.md
// (CREATING -> CREATED -> RUNNING -> EXITED|FAILED) and the synchronized
// re-exec architecture in runtime.md §1-§3. Starting in Phase 2
// (docs/adr/0006), the container's init (glider-init) remains PID 1 for
// the container's whole life and durably records the workload's eventual
// EXITED transition itself; Run's role after RUNNING is to forward
// external lifecycle signals to glider-init and read back what it
// recorded, not to interpret the workload's outcome directly.
//
// A value received on stop (typically SIGTERM or SIGINT, forwarded by
// cmd/glider-runtime from the process's own signal handling) is forwarded
// to glider-init as-is (STOPPING) — preserving which signal it actually
// was, not normalized to a single "please stop" — which itself forwards it
// to the workload's process group and owns the grace-period/SIGKILL
// escalation (docs/adr/0006, config.go's StopGrace). Run keeps its own,
// strictly longer, outer backstop deadline in case glider-init itself
// hangs or is unresponsive — see waitOrStop. Closing stop without sending
// a value is equivalent to never requesting a stop.
//
// The returned exit code is the workload's own exit status (or, if it was
// terminated by a signal, 128+signal) — never assumed to be 0.
func Run(stop <-chan os.Signal, cfg Config) (exitCode int, err error) {
	if err := requireCgroupV2(); err != nil {
		return 0, err
	}

	dir := state.Dir(cfg.stateDir(), cfg.ContainerID)
	now := time.Now()
	rec := state.Record{
		ContainerID: cfg.ContainerID,
		RootFS:      cfg.RootFS,
		Argv:        cfg.Argv,
		Hostname:    cfg.Hostname,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// The container directory is created here, exactly once, as part of
	// the ABSENT -> CREATING transition — the only point a container's
	// on-disk existence begins (container-lifecycle.md §3). Locking
	// (inside saveTransition) deliberately does not create it on demand —
	// see state.TryLock's doc comment for why.
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// No state transition is possible yet (ABSENT only ever legally
		// moves to CREATING — state.ValidTransition), so this is returned
		// directly rather than routed through failLaunch/transitionFailed.
		return 0, fmt.Errorf("create container state directory: %w", err)
	}

	if err := saveTransition(dir, &rec, state.Creating); err != nil {
		return 0, err
	}

	readyR, readyW, err := os.Pipe()
	if err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("create ready pipe: %w", err))
	}
	goR, goW, err := os.Pipe()
	if err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("create go pipe: %w", err))
	}
	resultR, resultW, err := os.Pipe()
	if err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("create result pipe: %w", err))
	}

	stopGrace := cfg.stopGrace()

	cmd := exec.Command("/proc/self/exe", append([]string{ReexecArg}, cfg.Argv...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		envRootFS+"="+cfg.RootFS,
		envHostname+"="+cfg.Hostname,
		envStateDir+"="+cfg.stateDir(),
		envContainerID+"="+cfg.ContainerID,
		envStopGrace+"="+stopGrace.String(),
	)
	// Order fixes fd 3/4/5 in the child; must match config.go's
	// fdReadyWrite/fdGoRead/fdResultWrite constants that init.go reads.
	cmd.ExtraFiles = []*os.File{readyW, goR, resultW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: namespace.Phase1Flags()}

	if err := cmd.Start(); err != nil {
		readyR.Close()
		readyW.Close()
		goR.Close()
		goW.Close()
		resultR.Close()
		resultW.Close()
		return failLaunch(dir, &rec, fmt.Errorf("start container process: %w", err))
	}

	// The parent only needs its own ends from here; holding the child-side
	// ends open would prevent EOF from ever being observed on resultR/readyR.
	readyW.Close()
	goR.Close()
	resultW.Close()

	reap := func() { _ = cmd.Wait() }

	ready := make([]byte, 1)
	n, readyErr := readyR.Read(ready)
	readyR.Close()
	if n != 1 || readyErr != nil {
		msg, _ := io.ReadAll(resultR)
		resultR.Close()
		goW.Close()
		reap()
		out := parseResult(msg)
		reason := out.reason
		if reason == "" {
			reason = "container exited before completing setup"
		}
		return failLaunch(dir, &rec, fmt.Errorf("%s", reason))
	}

	// glider-init is confirmed alive (it just signaled ready) — this is
	// the one moment the launcher can capture its identity directly via a
	// syscall return value, which is why CREATED (not RUNNING) is where
	// InitPID/InitStartTime are recorded (container-lifecycle.md §5,
	// docs/adr/0006).
	initIdentity, err := CaptureProcessIdentity(cmd.Process.Pid)
	if err != nil {
		goW.Close()
		resultR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("capture container init identity: %w", err))
	}
	rec.InitPID = initIdentity.PID
	rec.InitStartTime = initIdentity.StartTime

	if err := saveTransition(dir, &rec, state.Created); err != nil {
		goW.Close()
		resultR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return 0, err
	}

	pauseAfterCreatedForTest()

	if _, err := goW.Write([]byte{1}); err != nil {
		resultR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("signal go-ahead: %w", err))
	}
	goW.Close()

	// Blocks until glider-init writes its one result message (FAIL or
	// RUNNING) and closes the channel — protocol.go.
	msg, _ := io.ReadAll(resultR)
	resultR.Close()
	out := parseResult(msg)
	if !out.running {
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("%s", out.reason))
	}

	// Best-effort: resolve the workload's host PID/start-time for
	// observability. Not load-bearing (state.Record.WorkloadPID's doc
	// comment) — its absence never blocks publishing RUNNING.
	if id, ok := resolveChildIdentity(rec.InitPID); ok {
		rec.WorkloadPID = id.PID
		rec.WorkloadStartTime = id.StartTime
	}

	if err := saveTransition(dir, &rec, state.Running); err != nil {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return 0, err
	}

	return waitOrStop(stop, cmd, dir, &rec, stopGrace)
}

// waitOrStop blocks until glider-init's own process exits, forwarding a
// requested stop as the actual signal received on stop to glider-init
// itself — glider-init owns forwarding that to the workload and the
// grace-period/SIGKILL escalation (docs/adr/0006, superviseLoop in
// init.go). Run keeps an outer backstop deadline (stopGrace +
// outerBackstopBuffer, config.go) strictly longer than glider-init's own,
// so under normal operation glider-init's internal escalation always
// finishes first; the backstop only matters if glider-init itself hangs.
func waitOrStop(stop <-chan os.Signal, cmd *exec.Cmd, dir string, rec *state.Record, stopGrace time.Duration) (int, error) {
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		return finishAfterInitExit(dir, rec, waitErr)

	case sig := <-stop:
		if err := saveTransition(dir, rec, state.Stopping); err != nil {
			return 0, err
		}
		forward := syscall.SIGTERM
		if s, ok := sig.(syscall.Signal); ok {
			forward = s
		}
		_ = syscall.Kill(cmd.Process.Pid, forward)

		outerDeadline := stopGrace + outerBackstopBuffer
		select {
		case waitErr := <-waitDone:
			return finishAfterInitExit(dir, rec, waitErr)
		case <-time.After(outerDeadline):
			// glider-init failed to exit within its own escalation window
			// plus a buffer — a hung/unresponsive supervisor, the backstop
			// this doc comment describes. Killing it directly tears down
			// its whole PID namespace (docs/adr/0006's kernel guarantee),
			// so this is still a safe, if forceful, last resort.
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
			waitErr := <-waitDone
			return finishAfterInitExit(dir, rec, waitErr)
		}
	}
}

// finishAfterInitExit is reached once glider-init's own process has
// exited, regardless of its raw OS wait status — that status is
// deliberately not interpreted as the workload's outcome (docs/adr/0006).
// Instead it re-reads the state file glider-init should have durably
// written itself (init.go's persistExited) before exiting.
func finishAfterInitExit(dir string, rec *state.Record, initWaitErr error) (int, error) {
	loaded, err := state.Load(dir)
	if err == nil {
		*rec = loaded
		if rec.Phase == state.Exited {
			if rec.ExitCode == nil {
				return 0, &SupervisorFailureError{Err: fmt.Errorf("container recorded EXITED with no exit code")}
			}
			return *rec.ExitCode, nil
		}
	} else if !os.IsNotExist(err) {
		return 0, &SupervisorFailureError{Err: fmt.Errorf("container init exited and its state could not be read: %w", err)}
	}

	// glider-init exited without durably recording EXITED itself — a
	// Glider runtime failure (supervisor crash/kill), distinct from a
	// workload failure: the workload's real fate is unknown. Apply the
	// same recovery-decision logic used after a cold restart to converge
	// the state record safely (recovery.go), marking the exit code
	// inferred rather than fabricating a specific one.
	if convErr := convergeAfterUnexpectedInitExit(dir, rec); convErr != nil {
		return 0, &SupervisorFailureError{Err: fmt.Errorf("container init exited unexpectedly (%v) and could not be converged safely: %w", initWaitErr, convErr)}
	}
	return 0, &SupervisorFailureError{Err: fmt.Errorf("container supervisor exited before recording the workload's outcome (inferred EXITED, real exit code unknown)")}
}

// convergeAfterUnexpectedInitExit applies the RUNNING/STOPPING recovery
// decision inline (see recoverRunning, recovery.go) for the case where the
// launcher itself is still alive to observe glider-init's unexpected exit,
// rather than requiring a separate `recover` invocation for this common
// path. It reuses the same lock + identity-check + transition logic.
func convergeAfterUnexpectedInitExit(dir string, rec *state.Record) error {
	lock, err := state.LockWithTimeout(dir, lockTimeout)
	if err != nil {
		return fmt.Errorf("acquire container state lock: %w", err)
	}
	defer lock.Unlock()

	loaded, err := state.Load(dir)
	if err != nil {
		return fmt.Errorf("reload container state: %w", err)
	}
	if loaded.Phase == state.Exited {
		*rec = loaded
		return nil
	}

	loaded.ExitCode = nil
	loaded.ExitedInferred = true
	if err := applyTransition(&loaded, state.Exited); err != nil {
		return err
	}
	if err := state.Save(dir, loaded); err != nil {
		return fmt.Errorf("persist inferred EXITED state: %w", err)
	}
	*rec = loaded
	return nil
}

// transitionFailed records a FAILED transition (container-lifecycle.md:
// "no user process ever executed") with cause as the recorded error, and
// returns cause unchanged so callers can propagate it as Run's error.
func transitionFailed(dir string, rec *state.Record, cause error) error {
	rec.Error = cause.Error()
	if err := saveTransition(dir, rec, state.Failed); err != nil {
		return err
	}
	return cause
}

func failLaunch(dir string, rec *state.Record, cause error) (int, error) {
	return 0, transitionFailed(dir, rec, cause)
}
