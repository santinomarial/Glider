//go:build linux

// Package integration exercises the Phase 1/2 exit gates defined in
// docs/design/runtime.md §6 and the Phase 2 brief, black-box where
// practical: it builds the real glider-runtime and glider-test-helper
// binaries and runs them exactly as an operator would, rather than calling
// internal package functions directly. Recovery tests call
// internal/runtime/process's Recover directly, matching Phase 2 §14's
// choice of an internal recovery API over a required user-facing command.
// It requires root (or equivalent CAP_SYS_ADMIN-class privilege) and a
// Linux kernel with cgroup v2 — see requireRoot/requireCgroupV2 below and
// runtime.md §6 "test environment".
package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/santinomarial/glider/internal/runtime/process"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

var (
	buildOnce sync.Once
	buildErr  error
	gliderBin string
	helperBin string
)

// buildBinaries compiles the actual glider-runtime CLI and the test-helper
// fixture from source once per test run. Both are plain Go builds, not
// part of the state under test, so building them isn't itself part of any
// timing assertion below.
func buildBinaries(t *testing.T) (glider, helper string) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "glider-integration-bin-")
		if err != nil {
			buildErr = err
			return
		}

		gliderBin = filepath.Join(dir, "glider-runtime")
		buildRuntime := exec.Command("go", "build", "-o", gliderBin, "../../../cmd/glider-runtime")
		buildRuntime.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := buildRuntime.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build glider-runtime: %w\n%s", err, out)
			return
		}

		helperBin = filepath.Join(dir, "glider-test-helper")
		cmd := exec.Command("go", "build", "-o", helperBin, "./testhelper")
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err = cmd.CombinedOutput()
		if err != nil {
			buildErr = fmt.Errorf("build glider-test-helper: %w\n%s", err, out)
			return
		}
	})
	if buildErr != nil {
		t.Fatalf("build fixtures: %v", buildErr)
	}
	return gliderBin, helperBin
}

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("requires root (CAP_SYS_ADMIN-class privilege) for namespace/mount/pivot_root operations — see docs/design/runtime.md §6 'test environment'")
	}
}

// newRootFS builds a fresh, minimal container root filesystem fixture
// containing only the statically-linked test helper at /bin/glider-test-helper.
// Everything else (/proc, /sys, /dev, ...) is created by glider-runtime
// itself (mount_linux.go).
func newRootFS(t *testing.T, helper string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(helper)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(root, "bin", "glider-test-helper")
	if err := os.WriteFile(dst, data, 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

type runResult struct {
	exitCode int
	stdout   string
	stderr   string
}

// runGlider runs the real glider-runtime binary as a subprocess, exactly
// as `sudo glider-runtime run ...` would from a shell, and returns once it
// exits. stateDir is always overridden to an isolated temp directory so
// tests never touch a real host's /var/lib/glider or collide with each
// other.
func runGlider(t *testing.T, ctx context.Context, glider, stateDir, rootfs string, extraArgs []string, workload ...string) (runResult, error) {
	t.Helper()
	args := []string{"run", "--rootfs", rootfs, "--hostname", "glider-test", "--state-dir", stateDir}
	args = append(args, extraArgs...)
	args = append(args, "--")
	args = append(args, workload...)

	cmd := exec.CommandContext(ctx, glider, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	res := runResult{stdout: stdout.String(), stderr: stderr.String()}
	if err == nil {
		res.exitCode = 0
		return res, nil
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		res.exitCode = exitErr.ExitCode()
		return res, nil
	}
	return res, err
}

// loadState reads the single container's state record written under
// stateDir, failing the test if there isn't exactly one.
func loadState(t *testing.T, stateDir string) state.Record {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one container state dir under %s, found %d", stateDir, len(entries))
	}
	rec, err := state.Load(filepath.Join(stateDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("load state: %v", err)
	}
	return rec
}

func containerID(t *testing.T, stateDir string) string {
	t.Helper()
	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly one container dir under %s, found %d", stateDir, len(entries))
	}
	return entries[0].Name()
}

// assertNoLeakedProcesses fails the test if any process on the host still
// has cmdline referencing bin. A short grace period tolerates the kernel's
// ordinary asynchronous zombie-reap timing (Phase 2 §36 "no leaked
// processes").
func assertNoLeakedProcesses(t *testing.T, bin string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		leaked := findProcessesByCmdlineSubstring(t, bin)
		if len(leaked) == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("leaked process(es) still referencing %s: pids %v", bin, leaked)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func findProcessesByCmdlineSubstring(t *testing.T, substr string) []int {
	t.Helper()
	entries, err := os.ReadDir("/proc")
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		if strings.Contains(string(cmdline), substr) {
			pids = append(pids, pid)
		}
	}
	return pids
}

// countHostMounts returns the number of entries in this process's own
// mountinfo — a proxy for "mounts visible on the host", used to assert
// repeated container runs don't leak host-visible mounts (Phase 2 §23).
func countHostMounts(t *testing.T) int {
	t.Helper()
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		t.Fatal(err)
	}
	return len(strings.Split(strings.TrimRight(string(data), "\n"), "\n"))
}

func waitForPhase(t *testing.T, stateDir string, want state.Phase, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		entries, err := os.ReadDir(stateDir)
		if err == nil && len(entries) == 1 {
			rec, err := state.Load(filepath.Join(stateDir, entries[0].Name()))
			if err == nil && (rec.Phase == want || rec.Phase == state.Exited || rec.Phase == state.Failed) {
				if rec.Phase == want {
					return nil
				}
				return fmt.Errorf("reached phase %q instead of %q", rec.Phase, want)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for phase %q", timeout, want)
}

// waitForFile polls (bounded, no fixed sleep-and-hope) for path to exist —
// used with the launcher's test-only "pause after CREATED" hook
// (process.envPauseAfterCreated / testhooks.go) so tests know precisely
// when CREATED has been durably published, per Phase 2 §16's requirement
// for deterministic synchronization instead of "sleep then hope".
func waitForFile(t *testing.T, path string, timeout time.Duration) error {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s waiting for %s", timeout, path)
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

func waitForProcessGone(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid %d still alive after %s", pid, timeout)
}

// --- exit gate (a)/(A): PID namespace isolation, and Phase 2's PID 1
// supervision model (docs/adr/0006): glider-init is PID 1, the workload is
// a supervised child (PID 2), not PID 1 itself. ---

func TestWorkloadIsSupervisedChildNotPID1(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "pid")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got == "1" {
		t.Errorf("workload-observed pid = %q, want != 1 (docs/adr/0006: glider-init, not the workload, is PID 1)", got)
	}

	rec := loadState(t, stateDir)
	if rec.InitPID == 0 {
		t.Fatal("InitPID was never recorded")
	}
	if rec.InitPID == 1 {
		t.Errorf("host-observed InitPID recorded as 1 — expected the launcher's (host-namespace) view to differ from glider-init's own namespace-internal PID 1")
	}
	if rec.WorkloadPID != 0 && rec.WorkloadPID == rec.InitPID {
		t.Errorf("WorkloadPID (%d) == InitPID (%d): glider-init and the workload must be distinct processes", rec.WorkloadPID, rec.InitPID)
	}
	if rec.Phase != state.Exited {
		t.Errorf("final phase = %q, want EXITED", rec.Phase)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 0 {
		t.Errorf("exit code recorded = %v, want 0", rec.ExitCode)
	}
}

// --- exit gate (b): /proc inside shows only the container's own tree:
// glider-init (PID 1) plus the workload (some PID > 1) in Phase 2's model.
//
// The workload's exact namespace PID is NOT asserted as "2": the Go
// runtime backing glider-init spins up its own OS threads (GC, sysmon,
// goroutine scheduling — runtime.md §1's own rationale for why Phase 1
// needed re-exec to *become* PID 1 at all) after main() starts, and each
// such thread consumes a number from the same kernel-wide task-ID space
// non-deterministically before glider-init forks the workload — even
// though those threads never appear as their own top-level /proc entries
// (only thread-group leaders do). What's actually guaranteed, and what
// this test checks, is exactly two *processes* visible: PID 1 and one
// other, non-1 PID. ---

func assertOnlyInitAndWorkloadVisible(t *testing.T, got string) {
	t.Helper()
	parts := strings.Split(got, ",")
	if len(parts) != 2 {
		t.Fatalf("/proc inside container listed pids %q, want exactly 2 entries (glider-init + workload)", got)
	}
	if parts[0] != "1" {
		t.Errorf("/proc inside container: first pid = %q, want \"1\" (glider-init)", parts[0])
	}
	n, err := strconv.Atoi(parts[1])
	if err != nil || n <= 1 {
		t.Errorf("/proc inside container: second pid = %q, want a valid pid > 1 (the workload)", parts[1])
	}
}

func TestProcShowsOnlyContainerProcessTree(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "procs")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	assertOnlyInitAndWorkloadVisible(t, strings.TrimSpace(res.stdout))
}

// UTS isolation: hostname change inside must not leak to the host.

func TestUTSHostnameIsolation(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	hostBefore, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "hostname")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "glider-test" {
		t.Errorf("container-observed hostname = %q, want \"glider-test\"", got)
	}

	hostAfter, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if hostAfter != hostBefore {
		t.Errorf("host hostname changed from %q to %q — UTS namespace leaked to host", hostBefore, hostAfter)
	}
}

// --- exit gate (c): root filesystem is not the host's ---

func TestRootFilesystemIsolation(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	hostCanary := "/glider-host-canary-" + t.Name()
	if err := os.WriteFile(hostCanary, []byte("host-only"), 0o644); err != nil {
		t.Fatalf("create host canary: %v", err)
	}
	t.Cleanup(func() { os.Remove(hostCanary) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "stat", hostCanary)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "MISSING" {
		t.Errorf("host-only path visible inside container: stat reported %q, want MISSING", got)
	}

	res, err = runGlider(t, context.Background(), glider, stateDir+"-2", root, nil, "/bin/glider-test-helper", "write", "/inside-marker", "hello")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	got, err := os.ReadFile(filepath.Join(root, "inside-marker"))
	if err != nil {
		t.Fatalf("expected write from inside the container to land in the shared rootfs dir: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("inside-marker content = %q, want %q", got, "hello")
	}
}

// --- exit gate (e)/(F): exit status propagation, now through glider-init
// (docs/adr/0006) rather than the launcher observing its own child's raw
// wait status directly. ---

func TestExitCodePropagation(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cases := []int{0, 37, 1}
	for _, code := range cases {
		code := code
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			stateDir := t.TempDir()
			res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "exit", strconv.Itoa(code))
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.exitCode != code {
				t.Errorf("glider-runtime exit code = %d, want %d", res.exitCode, code)
			}
			rec := loadState(t, stateDir)
			if rec.Phase != state.Exited {
				t.Errorf("phase = %q, want EXITED (stderr: %s)", rec.Phase, res.stderr)
			}
			if rec.ExitCode == nil || *rec.ExitCode != code {
				t.Errorf("recorded exit code = %v, want %d", rec.ExitCode, code)
			}
			if rec.ExitedInferred {
				t.Errorf("ExitedInferred = true for a normal, observed exit")
			}
		})
	}
}

// --- exit gate (d)/(B): SIGTERM reaches and terminates the workload, now
// forwarded through glider-init to the workload's process group. ---

func TestSIGTERMReachesWorkload(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	args := []string{"run", "--rootfs", root, "--hostname", "glider-test", "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "trap-term", "/marker"}
	markerInRoot := filepath.Join(root, "marker")

	cmd := exec.Command(glider, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}

	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v (stderr: %s)", err, stderr.String())
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal glider-runtime: %v", err)
	}

	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	if elapsed > 5*time.Second {
		t.Errorf("SIGTERM handling took %s — suspiciously close to/over the default escalation grace period, suggests the signal wasn't forwarded promptly", elapsed)
	}

	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 0 {
			t.Fatalf("glider-runtime exit error = %v, stderr = %s", waitErr, stderr.String())
		}
	}

	if _, err := os.Stat(markerInRoot); err != nil {
		t.Errorf("workload's SIGTERM marker not found — signal did not reach the workload: %v", err)
	}

	rec := loadState(t, stateDir)
	if rec.Phase != state.Exited {
		t.Errorf("phase = %q, want EXITED", rec.Phase)
	}
}

// TestSIGTERMDefaultDispositionTerminatesWorkload is the direct proof of
// Phase 2's core value proposition (docs/adr/0006): unlike Phase 1 (where
// the workload itself was PID 1 and an *unhandled* SIGTERM is not
// delivered with default disposition by the kernel), the workload is now
// PID 2+ and a completely unmodified program — no signal handling
// whatsoever — stops correctly on SIGTERM via the kernel's normal default
// disposition once glider-init forwards it to the process group.
func TestSIGTERMDefaultDispositionTerminatesWorkload(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}

	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v (stderr: %s)", err, stderr.String())
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal glider-runtime: %v", err)
	}
	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	if elapsed > 2*time.Second {
		t.Errorf("default-disposition SIGTERM took %s to take effect — want well under a second, not anywhere near the 10s escalation grace", elapsed)
	}
	// The workload was terminated BY the (now-effective) default-disposition
	// SIGTERM, not exited cleanly — glider-runtime's own process exit code
	// mirrors that (128+SIGTERM), matching the workload's real outcome
	// rather than reporting 0 (Run's doc comment, container-lifecycle.md).
	wantExit := 128 + int(syscall.SIGTERM)
	if waitErr == nil {
		t.Fatalf("glider-runtime exited 0, want %d (workload killed by default-disposition SIGTERM)", wantExit)
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != wantExit {
		t.Fatalf("glider-runtime exit = %v, want exit code %d, stderr = %s", waitErr, wantExit, stderr.String())
	}

	rec := loadState(t, stateDir)
	if rec.Phase != state.Exited {
		t.Errorf("phase = %q, want EXITED", rec.Phase)
	}
	if rec.ExitCode == nil || *rec.ExitCode != wantExit {
		t.Errorf("recorded exit code = %v, want %d (128+SIGTERM, default-disposition termination)", rec.ExitCode, wantExit)
	}
}

// --- exit gate (C): grace-period escalation — a workload that ignores
// graceful termination is killed and reaped after the configured grace
// period. Uses --stop-grace to keep the test fast (Phase 2 §20). ---

func TestGracePeriodEscalatesToSIGKILL(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	// The marker path is a path from the *workload's* point of view — i.e.
	// relative to the container's own (pivoted) root, not a host path; per
	// the established pattern (TestSIGTERMReachesWorkload et al.), a write
	// under the container's root lands in the shared rootfs dir on the
	// host side too (no OverlayFS until Phase 5-7), checked below via
	// filepath.Join(root, "ready").
	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--stop-grace", "300ms",
		"--", "/bin/glider-test-helper", "ignore-term", "/ready")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}

	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v (stderr: %s)", err, stderr.String())
	}
	if err := waitForFile(t, filepath.Join(root, "ready"), 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("workload never confirmed its SIGTERM/SIGINT ignore handlers were installed: %v", err)
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal glider-runtime: %v", err)
	}
	waitErr := cmd.Wait()
	elapsed := time.Since(start)

	// Must take at least roughly the grace period (proves escalation
	// waited, not killed immediately) but stay well bounded (proves it
	// didn't hang past the launcher's own outer backstop).
	if elapsed < 250*time.Millisecond {
		t.Errorf("escalation happened after only %s — suspiciously fast for a 300ms grace period, suggests SIGKILL wasn't actually gated on the grace timer", elapsed)
	}
	if elapsed > 5*time.Second {
		t.Errorf("escalation took %s — want well under the outer backstop", elapsed)
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 128+int(syscall.SIGKILL) {
			t.Fatalf("glider-runtime exit code = %v, want %d (128+SIGKILL): stderr = %s", waitErr, 128+int(syscall.SIGKILL), stderr.String())
		}
	} else {
		t.Fatalf("expected a nonzero exit (workload force-killed), got 0")
	}

	rec := loadState(t, stateDir)
	if rec.Phase != state.Exited {
		t.Errorf("phase = %q, want EXITED", rec.Phase)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 128+int(syscall.SIGKILL) {
		t.Errorf("recorded exit code = %v, want %d", rec.ExitCode, 128+int(syscall.SIGKILL))
	}
}

// TestSIGINTAlsoTriggersGracefulStop proves the signal policy (signals.go)
// applies uniformly to SIGINT, not only SIGTERM.
func TestSIGINTAlsoTriggersGracefulStop(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v (stderr: %s)", err, stderr.String())
	}

	start := time.Now()
	if err := cmd.Process.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("signal glider-runtime: %v", err)
	}
	waitErr := cmd.Wait()
	if time.Since(start) > 2*time.Second {
		t.Errorf("SIGINT handling took too long — want prompt default-disposition termination")
	}
	if waitErr != nil {
		if exitErr, ok := waitErr.(*exec.ExitError); !ok || exitErr.ExitCode() != 128+int(syscall.SIGINT) {
			t.Fatalf("glider-runtime exit code = %v, want %d", waitErr, 128+int(syscall.SIGINT))
		}
	}
}

// --- exit gate (D)/(E): zombie reaping and orphan handling ---

func TestZombieChurnLeavesNoLeakedProcesses(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "zombie-churn")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "SPAWNED" {
		t.Errorf("stdout = %q, want SPAWNED", got)
	}
	rec := loadState(t, stateDir)
	if rec.Phase != state.Exited {
		t.Errorf("phase = %q, want EXITED", rec.Phase)
	}
	// assertNoLeakedProcesses (Cleanup) is the real assertion: it fails if
	// glider-init didn't reap the 20 unwaited children reparented to it
	// when the workload exited.
}

func TestOrphanIsReparentedToInit(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "orphan-churn")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "REPARENTED" {
		t.Errorf("stdout = %q, want REPARENTED (grandchild's PPid never became 1)", got)
	}
	// assertNoLeakedProcesses (Cleanup) confirms the still-sleeping
	// grandchild didn't survive container teardown either.
}

// --- exit gate (f): concurrent runs get independent PID namespaces.
//
// Renamed from Phase 1's TestConcurrentRunsDoNotInterfere (Phase 2 §33
// audit finding): the original name implied more than the test proves.
// Phase 1/2 bind-mount the operator-supplied --rootfs directly (no
// OverlayFS until Phase 5-7, ADR-0004) — concurrent *writes* to a shared
// rootfs are NOT isolated and can conflict; this test only exercises
// independent namespace/process lifecycle and unique container IDs, which
// is what its name says now. ---

func TestConcurrentRunsGetIndependentPIDNamespaces(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	const n = 4
	type outcome struct {
		res      runResult
		stateDir string
		err      error
	}
	results := make(chan outcome, n)

	for i := 0; i < n; i++ {
		i := i
		go func() {
			stateDir, mkErr := os.MkdirTemp("", fmt.Sprintf("glider-concurrent-%d-", i))
			if mkErr != nil {
				results <- outcome{err: mkErr}
				return
			}
			res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "procs")
			results <- outcome{res: res, stateDir: stateDir, err: err}
		}()
	}

	seenContainerIDs := make(map[string]bool)
	for i := 0; i < n; i++ {
		o := <-results
		if o.err != nil {
			t.Fatalf("run %d: %v", i, o.err)
		}
		if o.res.exitCode != 0 {
			t.Fatalf("run %d exit code = %d, stderr = %s", i, o.res.exitCode, o.res.stderr)
		}
		assertOnlyInitAndWorkloadVisible(t, strings.TrimSpace(o.res.stdout))
		rec := loadState(t, o.stateDir)
		if seenContainerIDs[rec.ContainerID] {
			t.Errorf("duplicate container id %q across concurrent runs", rec.ContainerID)
		}
		seenContainerIDs[rec.ContainerID] = true
		if rec.Phase != state.Exited {
			t.Errorf("run %d: phase = %q, want EXITED", i, rec.Phase)
		}
	}
}

// --- invalid workload returns a clear error ---

func TestInvalidWorkloadReturnsClearError(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/does-not-exist")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode == 0 {
		t.Fatalf("expected non-zero exit for a nonexistent workload command")
	}
	if !strings.Contains(res.stderr, "does-not-exist") {
		t.Errorf("stderr does not clearly name the failing command: %q", res.stderr)
	}

	rec := loadState(t, stateDir)
	if rec.Phase != state.Failed {
		t.Errorf("phase = %q, want FAILED", rec.Phase)
	}
	if rec.Error == "" {
		t.Errorf("expected a non-empty recorded error reason")
	}
}

// --- exit gate (M): no host-visible mount leaks across repeated runs ---

func TestNoMountLeaksAcrossRepeatedRuns(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	before := countHostMounts(t)
	const n = 10
	for i := 0; i < n; i++ {
		stateDir := t.TempDir()
		res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "exit", "0")
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if res.exitCode != 0 {
			t.Fatalf("run %d: exit code = %d, stderr = %s", i, res.exitCode, res.stderr)
		}
	}
	after := countHostMounts(t)
	if after != before {
		t.Errorf("host mount count changed from %d to %d across %d container start/exit cycles — mount leak", before, after, n)
	}
}

// --- exit gate (I): launcher dies after CREATED; recovery converges
// safely (Phase 1's deferred exit-gate test, closed in Phase 2 §16). ---

func TestLauncherDeathAfterCreatedRecoversToFailed(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "created-marker")

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	cmd.Env = append(os.Environ(), "_GLIDER_TEST_PAUSE_AFTER_CREATED="+marker)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}

	// Deterministic sync point: the marker file is only ever written after
	// the launcher's CREATED write has been fsynced and renamed into place
	// (testhooks.go) — no sleep-and-hope involved.
	if err := waitForFile(t, marker, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("launcher never reached the post-CREATED pause: %v (stderr: %s)", err, stderr.String())
	}

	rec := loadState(t, stateDir)
	if rec.Phase != state.Created {
		t.Fatalf("phase at pause = %q, want CREATED", rec.Phase)
	}
	if rec.InitPID == 0 {
		t.Fatal("InitPID not recorded at CREATED")
	}
	initPID := rec.InitPID
	t.Cleanup(func() {
		// Best-effort: recovery below should already have converged this,
		// but guarantee no leak regardless of assertion order/failures.
		_ = syscall.Kill(initPID, syscall.SIGKILL)
		assertNoLeakedProcesses(t, helper)
	})

	// Kill the launcher itself (not glider-init) — simulating the exact
	// Phase 1 §6 exit-gate (g) / Phase 2 §16 scenario.
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()

	// glider-init notices its "go" pipe closed (EOF) and self-terminates —
	// docs/adr/0006, init.go's goR.Read doc comment.
	waitForProcessGone(t, initPID, 5*time.Second)

	id := containerID(t, stateDir)
	result, err := process.Recover(stateDir, id)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if result.Action != process.RecoveryConvergedFailed {
		t.Errorf("recovery action = %q, want CONVERGED_FAILED", result.Action)
	}
	if result.Record.Phase != state.Failed {
		t.Errorf("recovered phase = %q, want FAILED", result.Record.Phase)
	}
}

// --- exit gate (J): launcher dies while RUNNING; glider-init keeps
// supervising independently (docs/adr/0006's "no re-adoption needed"
// policy), and recovery correctly observes it as still healthy. ---

func TestLauncherDeathWhileRunningLeavesContainerAlive(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	// Stderr is deliberately connected to this test binary's own os.Stderr
	// (a real *os.File), not a bytes.Buffer: with a buffer, Cmd.Wait()
	// also waits for its internal pipe-copying goroutine to see EOF, which
	// would never happen until every process holding an inherited dup of
	// that pipe (glider-init AND the workload — both inherit the
	// launcher's stdio, launcher.go) has exited. That's exactly what this
	// test deliberately keeps alive past the launcher's own death, so
	// Wait() below must not depend on it.
	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}

	rec := loadState(t, stateDir)
	initPID := rec.InitPID

	if err := cmd.Process.Kill(); err != nil { // SIGKILL the launcher, not glider-init
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()

	// glider-init must still be alive — it does not depend on its launcher
	// parent (docs/adr/0006).
	time.Sleep(200 * time.Millisecond)
	if !processAlive(initPID) {
		t.Fatalf("glider-init (pid %d) died along with its launcher — it must supervise independently", initPID)
	}

	id := containerID(t, stateDir)
	result, err := process.Recover(stateDir, id)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if result.Action != process.RecoveryStillHealthy {
		t.Errorf("recovery action = %q, want STILL_HEALTHY (docs/adr/0006: no re-adoption needed, glider-init is already self-supervising)", result.Action)
	}
	if result.Record.Phase != state.Running {
		t.Errorf("recovered phase = %q, want RUNNING (still genuinely running)", result.Record.Phase)
	}

	// Clean up directly against glider-init's durably-recorded host PID,
	// simulating what a future gliderd would eventually do.
	_ = syscall.Kill(initPID, syscall.SIGTERM)
	waitForProcessGone(t, initPID, 5*time.Second)
}

// --- exit gate (K)/(L): idempotent, safely-serialized recovery ---

func TestRecoveryIsIdempotent(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "created-marker")

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	cmd.Env = append(os.Environ(), "_GLIDER_TEST_PAUSE_AFTER_CREATED="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForFile(t, marker, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("launcher never reached the post-CREATED pause: %v", err)
	}
	rec := loadState(t, stateDir)
	initPID := rec.InitPID
	t.Cleanup(func() {
		_ = syscall.Kill(initPID, syscall.SIGKILL)
		assertNoLeakedProcesses(t, helper)
	})

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()
	waitForProcessGone(t, initPID, 5*time.Second)

	id := containerID(t, stateDir)

	r1, err := process.Recover(stateDir, id)
	if err != nil {
		t.Fatalf("Recover #1: %v", err)
	}
	if r1.Action != process.RecoveryConvergedFailed {
		t.Fatalf("Recover #1 action = %q, want CONVERGED_FAILED", r1.Action)
	}

	r2, err := process.Recover(stateDir, id)
	if err != nil {
		t.Fatalf("Recover #2: %v", err)
	}
	if r2.Action != process.RecoveryCleanedUp {
		t.Fatalf("Recover #2 action = %q, want CLEANED_UP (FAILED -> DELETING -> ABSENT)", r2.Action)
	}

	_, err = process.Recover(stateDir, id)
	if !errors.Is(err, process.ErrContainerNotFound) {
		t.Fatalf("Recover #3 (after full convergence): got %v, want ErrContainerNotFound", err)
	}
}

func TestConcurrentRecoveryDoesNotRaceDestructively(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "created-marker")

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	cmd.Env = append(os.Environ(), "_GLIDER_TEST_PAUSE_AFTER_CREATED="+marker)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForFile(t, marker, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("launcher never reached the post-CREATED pause: %v", err)
	}
	rec := loadState(t, stateDir)
	initPID := rec.InitPID
	t.Cleanup(func() {
		_ = syscall.Kill(initPID, syscall.SIGKILL)
		assertNoLeakedProcesses(t, helper)
	})

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()
	waitForProcessGone(t, initPID, 5*time.Second)

	id := containerID(t, stateDir)

	const n = 5
	var wg sync.WaitGroup
	errs := make([]error, n)
	actions := make([]process.RecoveryAction, n)
	for i := 0; i < n; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := process.Recover(stateDir, id)
			if err != nil {
				errs[i] = err
				return
			}
			actions[i] = result.Action
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil && !errors.Is(err, process.ErrContainerNotFound) {
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}
	if _, err := os.Stat(filepath.Join(stateDir, id)); !os.IsNotExist(err) {
		t.Errorf("container state dir still exists after concurrent recovery converged it: %v", err)
	}
}

// --- exit gate (N): state corruption safety at the integration level
// (unit-level coverage in internal/runtime/process/state's test suite) ---

func TestRecoveryFailsSafelyOnCorruptState(t *testing.T) {
	requireRoot(t)
	stateDir := t.TempDir()
	id := "corrupt-container"
	dir := filepath.Join(stateDir, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte("{not valid json"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := process.Recover(stateDir, id)
	if err == nil {
		t.Fatal("expected Recover to fail on corrupt state, got nil error")
	}
	if !errors.Is(err, state.ErrCorruptState) {
		t.Errorf("Recover error = %v, want it to wrap state.ErrCorruptState", err)
	}
}
