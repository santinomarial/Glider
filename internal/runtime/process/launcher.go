//go:build linux

package process

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/santinomarial/glider/internal/runtime/namespace"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// stopGrace is how long Run waits after forwarding SIGTERM before forcing
// SIGKILL. Phase 1 has no restart policy or reconciler (Phase 10/16) to
// tune this against real workload behavior, so a fixed, generous value is
// used rather than exposing a premature configuration knob.
const stopGrace = 10 * time.Second

// requireCgroupV2 fails fast on hosts without the cgroup v2 unified
// hierarchy (ADR-0001), even though Phase 1 does not itself write any
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

// Run launches a single Phase 1 container per cfg and blocks until it
// exits, following the parent-owned lifecycle in container-lifecycle.md
// (CREATING -> CREATED -> RUNNING -> EXITED|FAILED) and the synchronized
// re-exec architecture in runtime.md §1-§3.
//
// Cancelling ctx forwards SIGTERM to the container (STOPPING), escalating
// to SIGKILL after stopGrace if it hasn't exited — this is the mechanism
// runtime.md §6 exit-gate (d) exercises, and the doc's honest caveat
// applies: an unhandled SIGTERM sent to a namespace's PID 1 is not
// delivered with default disposition by the kernel (runtime.md §5), so a
// workload that doesn't trap it will only stop at the SIGKILL escalation,
// not immediately. Full PID 1 signal semantics land in Phase 2.
//
// The returned exit code is the workload's own exit status (or, if it was
// terminated by a signal, 128+signal, the standard shell convention) —
// never assumed to be 0.
func Run(ctx context.Context, cfg Config) (exitCode int, err error) {
	if err := requireCgroupV2(); err != nil {
		return 0, err
	}

	dir := state.Dir(cfg.stateDir(), cfg.ContainerID)
	now := time.Now()
	rec := state.Record{
		ContainerID: cfg.ContainerID,
		RootFS:      cfg.RootFS,
		Argv:        cfg.Argv,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if err := transition(dir, &rec, state.Creating); err != nil {
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
	errR, errW, err := os.Pipe()
	if err != nil {
		return failLaunch(dir, &rec, fmt.Errorf("create error pipe: %w", err))
	}

	cmd := exec.Command("/proc/self/exe", append([]string{ReexecArg}, cfg.Argv...)...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(),
		envRootFS+"="+cfg.RootFS,
		envHostname+"="+cfg.Hostname,
	)
	// Order fixes fd 3/4/5 in the child; must match config.go's
	// fdReadyWrite/fdGoRead/fdErrWrite constants that init.go reads.
	cmd.ExtraFiles = []*os.File{readyW, goR, errW}
	cmd.SysProcAttr = &syscall.SysProcAttr{Cloneflags: namespace.Phase1Flags()}

	if err := cmd.Start(); err != nil {
		readyR.Close()
		readyW.Close()
		goR.Close()
		goW.Close()
		errR.Close()
		errW.Close()
		return failLaunch(dir, &rec, fmt.Errorf("start container process: %w", err))
	}

	// The parent only needs its own ends from here; holding the child-side
	// ends open would prevent EOF from ever being observed on errR/readyR.
	readyW.Close()
	goR.Close()
	errW.Close()

	reap := func() {
		_ = cmd.Wait()
	}

	ready := make([]byte, 1)
	n, readyErr := readyR.Read(ready)
	readyR.Close()
	if n != 1 || readyErr != nil {
		msg, _ := io.ReadAll(errR)
		errR.Close()
		goW.Close()
		reap()
		reason := strings.TrimSpace(string(msg))
		if reason == "" {
			reason = "container exited before completing setup"
		}
		return failLaunch(dir, &rec, fmt.Errorf("%s", reason))
	}

	if err := transition(dir, &rec, state.Created); err != nil {
		goW.Close()
		errR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return 0, err
	}

	if _, err := goW.Write([]byte{1}); err != nil {
		errR.Close()
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("signal go-ahead: %w", err))
	}
	goW.Close()

	// Blocks until either the child writes an error (pivot_root or exec
	// failure) or its errW fd auto-closes on a successful exec (CLOEXEC).
	msg, _ := io.ReadAll(errR)
	errR.Close()
	if len(msg) > 0 {
		reap()
		return failLaunch(dir, &rec, fmt.Errorf("%s", strings.TrimSpace(string(msg))))
	}

	pid := cmd.Process.Pid
	rec.Pid = pid
	if st, err := procStartTime(pid); err == nil {
		rec.PidStartTime = st
	}
	if err := transition(dir, &rec, state.Running); err != nil {
		_ = syscall.Kill(pid, syscall.SIGKILL)
		reap()
		return 0, err
	}

	return waitOrStop(ctx, cmd, dir, &rec)
}

// waitOrStop blocks until the container exits, forwarding a requested stop
// (via ctx cancellation) as SIGTERM-then-SIGKILL, per container-lifecycle.md
// STOPPING semantics ("waiting for exit or timeout").
func waitOrStop(ctx context.Context, cmd *exec.Cmd, dir string, rec *state.Record) (int, error) {
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	select {
	case waitErr := <-waitDone:
		return recordExit(dir, rec, waitErr)

	case <-ctx.Done():
		if err := transition(dir, rec, state.Stopping); err != nil {
			return 0, err
		}
		_ = syscall.Kill(cmd.Process.Pid, syscall.SIGTERM)

		select {
		case waitErr := <-waitDone:
			return recordExit(dir, rec, waitErr)
		case <-time.After(stopGrace):
			_ = syscall.Kill(cmd.Process.Pid, syscall.SIGKILL)
			waitErr := <-waitDone
			return recordExit(dir, rec, waitErr)
		}
	}
}

func recordExit(dir string, rec *state.Record, waitErr error) (int, error) {
	code := 0
	if waitErr != nil {
		exitErr, ok := waitErr.(*exec.ExitError)
		if !ok {
			// Not an ordinary nonzero-exit/signal termination — an OS-level
			// wait(2) failure, which Phase 1 has no retry/reconciler for.
			return 0, transitionFailed(dir, rec, fmt.Errorf("wait for container: %w", waitErr))
		}
		ws, ok := exitErr.Sys().(syscall.WaitStatus)
		if !ok {
			return 0, transitionFailed(dir, rec, fmt.Errorf("unrecognized wait status: %w", waitErr))
		}
		switch {
		case ws.Exited():
			code = ws.ExitStatus()
		case ws.Signaled():
			code = 128 + int(ws.Signal())
		default:
			code = -1
		}
	}

	rec.ExitCode = &code
	if err := transition(dir, rec, state.Exited); err != nil {
		return 0, err
	}
	return code, nil
}

// transition validates and persists a lifecycle move per
// container-lifecycle.md's transition table (state.ValidTransition), so an
// implementation bug that tries an illegal transition fails loudly instead
// of silently corrupting the recorded lifecycle.
func transition(dir string, rec *state.Record, to state.Phase) error {
	if !state.ValidTransition(rec.Phase, to) {
		return fmt.Errorf("invalid container lifecycle transition %q -> %q", rec.Phase, to)
	}
	rec.Phase = to
	rec.UpdatedAt = time.Now()
	if err := state.Save(dir, *rec); err != nil {
		return fmt.Errorf("persist %s state: %w", to, err)
	}
	return nil
}

func transitionFailed(dir string, rec *state.Record, cause error) error {
	rec.Error = cause.Error()
	if err := transition(dir, rec, state.Failed); err != nil {
		return err
	}
	return cause
}

func failLaunch(dir string, rec *state.Record, cause error) (int, error) {
	return 0, transitionFailed(dir, rec, cause)
}

// procStartTime reads field 22 (starttime) of /proc/<pid>/stat — the
// PID-reuse defense identity tuple described in container-lifecycle.md §5.
// The comm field (2nd, parenthesized) may itself contain spaces or
// parentheses, so parsing anchors on the last ')' rather than splitting
// naively on whitespace.
func procStartTime(pid int) (uint64, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return 0, fmt.Errorf("unexpected /proc/%d/stat format", pid)
	}
	fields := strings.Fields(s[i+2:])
	// Fields after comm start at field 3 (state); starttime is field 22
	// overall, i.e. index 22-3=19 in this slice.
	const starttimeIdx = 22 - 3
	if len(fields) <= starttimeIdx {
		return 0, fmt.Errorf("unexpected /proc/%d/stat field count", pid)
	}
	return strconv.ParseUint(fields[starttimeIdx], 10, 64)
}
