//go:build linux

package process

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"github.com/santinomarial/glider/internal/runtime/namespace"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

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
	// Discovery (finding the cgroup2 mount) has no side effects; the
	// actual delegation bootstrap (EnsureDelegated, which can move this
	// process — docs/design/cgroups.md "Delegation") happens below, after
	// CREATING is durably recorded, so a failure there is attributable to
	// a specific container's launch like every other setup step.
	mgr, err := cgroup.NewManager()
	if err != nil {
		return 0, err
	}

	dir := state.Dir(cfg.stateDir(), cfg.ContainerID)
	now := time.Now()
	cgroupPath, err := mgr.ContainerPathRelative(cfg.ContainerID)
	if err != nil {
		return 0, err
	}
	rec := state.Record{
		ContainerID: cfg.ContainerID,
		RootFS:      cfg.RootFS,
		Argv:        cfg.Argv,
		Hostname:    cfg.Hostname,
		Env:         append([]string(nil), cfg.Env...),
		WorkingDir:  cfg.WorkingDir,
		CgroupPath:  cgroupPath,
		Resources:   toStateResources(cfg.Resources),
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	// The container directory is created here, exactly once, as part of
	// the ABSENT -> CREATING transition — the only point a container's
	// on-disk existence begins (container-lifecycle.md §3). Locking
	// (inside saveTransition) deliberately does not create it on demand —
	// see state.TryLock's doc comment for why. The intended cgroup path is
	// recorded in this same durable write, before the cgroup itself is
	// created (container-lifecycle.md §3: "the record of intent is
	// durable before the resource exists").
	if err := os.MkdirAll(dir, 0o755); err != nil {
		// No state transition is possible yet (ABSENT only ever legally
		// moves to CREATING — state.ValidTransition), so this is returned
		// directly rather than routed through failLaunch/transitionFailed.
		return 0, fmt.Errorf("create container state directory: %w", err)
	}

	if err := saveTransition(dir, &rec, state.Creating); err != nil {
		return 0, err
	}

	if err := mgr.EnsureDelegated(); err != nil {
		return failLaunch(dir, &rec, err)
	}
	if _, err := mgr.Create(cfg.ContainerID, cfg.Resources); err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("create container cgroup: %w", err))
	}
	// From here on, a container cgroup exists on disk; every subsequent
	// failure path must remove it before returning (Phase 4 §35: "no
	// invalid configuration should leave a cgroup directory behind" —
	// extended to every launch failure, not just invalid config). This
	// single deferred cleanup covers every early-return below uniformly,
	// rather than repeating it at each one; it only acts if the launch
	// never reached RUNNING (launchSucceeded), since a live container's
	// cgroup must obviously survive the rest of Run's normal operation.
	launchSucceeded := false
	defer func() {
		if !launchSucceeded {
			cleanupContainerCgroup(mgr, cfg.ContainerID)
		}
	}()

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
	envJSON, err := json.Marshal(cfg.Env)
	if err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("encode workload environment: %w", err))
	}
	cmd.Env = append(os.Environ(),
		envRootFS+"="+cfg.RootFS,
		envHostname+"="+cfg.Hostname,
		envStateDir+"="+cfg.stateDir(),
		envContainerID+"="+cfg.ContainerID,
		envStopGrace+"="+stopGrace.String(),
		envImageEnv+"="+base64.StdEncoding.EncodeToString(envJSON),
		envWorkingDir+"="+cfg.WorkingDir,
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

	// Attach glider-init to the container cgroup now, before CREATED is
	// published and strictly before "go" is ever sent (Phase 4 §14's
	// critical invariant: no user code runs before cgroup membership and
	// limits are established). Every descendant glider-init forks from
	// here on — the workload, and anything it forks in turn — inherits
	// this membership automatically; nothing else needs to attach itself
	// (Phase 4 §14).
	if err := mgr.Attach(cfg.ContainerID, initIdentity.PID); err != nil {
		goW.Close()
		resultR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("attach container init to cgroup: %w", err))
	}
	if ok, err := mgr.VerifyAttached(cfg.ContainerID, initIdentity.PID); err != nil || !ok {
		goW.Close()
		resultR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		if err == nil {
			err = fmt.Errorf("glider-init's real cgroup membership does not match the container cgroup")
		}
		return failLaunch(dir, &rec, fmt.Errorf("verify cgroup attachment: %w", err))
	}
	if cfg.ConfigureNetwork != nil {
		if err := cfg.ConfigureNetwork(initIdentity.PID); err != nil {
			goW.Close()
			resultR.Close()
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
			reap()
			return failLaunch(dir, &rec, fmt.Errorf("configure container network: %w", err))
		}
	}

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
	if ok, err := VerifyProcessRoot(rec.InitPID, cfg.RootFS); err != nil || !ok {
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		if err == nil {
			err = fmt.Errorf("container init root does not match requested rootfs")
		}
		return failLaunch(dir, &rec, fmt.Errorf("verify container rootfs: %w", err))
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
	launchSucceeded = true

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
			// glider-init and every process that shared its cgroup are
			// confirmed gone (cmd.Wait() already returned) — safe to
			// remove the container cgroup now, as part of Run's own
			// normal shutdown, rather than deferring it to a later
			// explicit Recover call (Phase 4 §19; unlike Phase 1/2's
			// mounts, a cgroup does not self-clean and would otherwise
			// leak on every single ordinary container run).
			cleanupContainerCgroupByName(rec.ContainerID)
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
	cleanupContainerCgroupByName(rec.ContainerID)
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
