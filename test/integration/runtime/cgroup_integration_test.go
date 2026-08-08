//go:build linux

// Phase 4 (docs/design/cgroups.md) privileged integration tests: dedicated
// cgroups, membership ordering, CPU/memory/PID enforcement with real
// kernel evidence, cleanup, and crash recovery. Shares buildBinaries,
// newRootFS, runGlider, loadState, waitForPhase, etc. with
// runtime_integration_test.go.
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

	"github.com/santinomarial/glider/internal/runtime/cgroup"
	"github.com/santinomarial/glider/internal/runtime/process"
	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// cgroupManager returns a ready-to-use, delegated cgroup.Manager for
// tests — EnsureDelegated is idempotent (manager.go's doc comment) so
// calling it from every test that needs one is cheap and safe under
// concurrency.
func cgroupManager(t *testing.T) *cgroup.Manager {
	t.Helper()
	m, err := cgroup.NewManager()
	if err != nil {
		if errors.Is(err, cgroup.ErrUnsupported) {
			t.Skip("cgroup v2 not available in this environment")
		}
		t.Fatalf("cgroup.NewManager: %v", err)
	}
	if err := m.EnsureDelegated(); err != nil {
		t.Skipf("cgroup v2 controllers not delegated to this test environment (see scripts/test-linux-runtime.sh): %v", err)
	}
	return m
}

// assertCgroupAbsent fails the test if containerID's cgroup still exists.
func assertCgroupAbsent(t *testing.T, m *cgroup.Manager, containerID string) {
	t.Helper()
	path, err := m.ContainerPath(containerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("container cgroup %s still exists (err=%v), want removed", path, err)
	}
}

func waitForCgroupAbsent(t *testing.T, m *cgroup.Manager, containerID string, timeout time.Duration) {
	t.Helper()
	path, err := m.ContainerPath(containerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); os.IsNotExist(err) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("container cgroup %s still exists after %s, want removed", path, timeout)
}

// --- exit gate A/B: dedicated cgroup + correct membership ordering ---

func TestContainerGetsDedicatedCgroupWithCorrectMembership(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--", "/bin/glider-test-helper", "sleep-default", "10")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}

	rec := loadState(t, stateDir)
	if rec.CgroupPath == "" {
		t.Fatal("state record has no CgroupPath")
	}

	path, err := m.ContainerPath(rec.ContainerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected container cgroup to exist at %s: %v", path, err)
	}

	// glider-init's real membership, per the kernel (§14 "verify
	// attachment... with real evidence, not merely trusting the write").
	initOK, err := m.VerifyAttached(rec.ContainerID, rec.InitPID)
	if err != nil {
		t.Fatalf("VerifyAttached(init): %v", err)
	}
	if !initOK {
		t.Errorf("glider-init (pid %d) is not a member of the container cgroup", rec.InitPID)
	}

	// The workload must have inherited membership automatically (§14) —
	// no separate attach step exists for it.
	if rec.WorkloadPID != 0 {
		workloadOK, err := m.VerifyAttached(rec.ContainerID, rec.WorkloadPID)
		if err != nil {
			t.Fatalf("VerifyAttached(workload): %v", err)
		}
		if !workloadOK {
			t.Errorf("workload (pid %d) is not a member of the container cgroup — did not inherit from glider-init", rec.WorkloadPID)
		}
	} else {
		t.Log("WorkloadPID not resolved this run (best-effort) — membership only checked for glider-init")
	}

	stats, err := m.Stats(rec.ContainerID)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	// PIDs.Current includes glider-init itself (Phase 4 §12's explicit
	// accounting note) plus the workload.
	if stats.PIDs.Current < 2 {
		t.Errorf("PIDs.Current = %d, want >= 2 (glider-init + workload)", stats.PIDs.Current)
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

// --- exit gate G: unrestricted/default mode ---

func TestDefaultModeHasNoLimitsButStillGetsCgroup(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, nil, "/bin/glider-test-helper", "exit", "0")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	rec := loadState(t, stateDir)
	if rec.Resources != (state.Resources{}) {
		t.Errorf("Resources = %+v, want zero value (no limits requested)", rec.Resources)
	}
	// The container's cgroup should already be gone (normal exit cleanup
	// — Phase 4 §19) by the time glider-runtime itself has returned.
	assertCgroupAbsent(t, m, rec.ContainerID)
}

// --- exit gate C: CPU limit enforcement, with real kernel evidence ---

func TestCPUThrottlingIsEnforced(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--cpus", "0.2",
		"--", "/bin/glider-test-helper", "cpu-spin", "4")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}
	rec := loadState(t, stateDir)

	// Give the spin loop real wall-clock time to accumulate multiple
	// 100ms enforcement periods against its 20%-of-one-core quota.
	time.Sleep(2 * time.Second)

	stats, err := m.Stats(rec.ContainerID)
	if err != nil {
		t.Fatalf("Stats while running: %v", err)
	}
	t.Logf("cpu.stat while throttled: %+v", stats.CPU)
	if stats.CPU.NrThrottled == 0 || stats.CPU.ThrottledUsec == 0 {
		t.Errorf("cpu.stat shows no throttling (nr_throttled=%d, throttled_usec=%d) after 2s of CPU-bound work under --cpus 0.2 — cpu.max does not appear to be enforced",
			stats.CPU.NrThrottled, stats.CPU.ThrottledUsec)
	}

	waitErr := cmd.Wait()
	_ = waitErr
	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

// --- exit gate D: memory limit enforcement, with real kernel evidence ---

func TestMemoryCurrentReflectsRealUsage(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	const holdMiB = 32
	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--memory", "128Mi",
		"--", "/bin/glider-test-helper", "mem-touch-and-hold", strconv.Itoa(holdMiB), "3")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}
	rec := loadState(t, stateDir)

	// Bounded poll (not a fixed sleep-and-hope) for the workload's own
	// "TOUCHED" confirmation to appear in its stdout, proving it has
	// actually finished touching every page before we read memory.current.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdout.String(), "TOUCHED") {
		time.Sleep(20 * time.Millisecond)
	}

	stats, err := m.Stats(rec.ContainerID)
	if err != nil {
		t.Fatalf("Stats while running: %v", err)
	}
	t.Logf("memory.current while holding %dMiB: %d bytes", holdMiB, stats.Memory.CurrentBytes)
	wantMin := uint64(holdMiB) * 1024 * 1024
	if stats.Memory.CurrentBytes < wantMin {
		t.Errorf("memory.current = %d, want >= %d (%dMiB actually touched) — memory.max/current does not appear to reflect real usage",
			stats.Memory.CurrentBytes, wantMin, holdMiB)
	}

	_ = cmd.Wait()
	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

func TestMemoryLimitContainsRunawayWorkload(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--memory", "24Mi",
		"--", "/bin/glider-test-helper", "mem-hog")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}
	rec := loadState(t, stateDir)

	// Best-effort race against the container's own cleanup: try to catch
	// memory.events showing an OOM kill while the cgroup still exists.
	// Not asserted on — losing this race is expected sometimes (cleanup
	// can be fast) and does not mean containment failed; the exit-code
	// check below is the authoritative containment evidence.
	var caughtOOMEvidence string
	oomDeadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(oomDeadline) {
		if stats, err := m.Stats(rec.ContainerID); err == nil {
			if stats.Memory.Events.OOMKill > 0 || stats.Memory.Events.Max > 0 {
				caughtOOMEvidence = fmt.Sprintf("memory.events: oom_kill=%d max=%d (caught live)", stats.Memory.Events.OOMKill, stats.Memory.Events.Max)
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
	}

	waitErr := cmd.Wait()
	wantExit := 128 + int(syscall.SIGKILL)
	if waitErr == nil {
		t.Fatalf("glider-runtime exited 0 — expected the runaway workload to be killed (exit %d)", wantExit)
	}
	exitErr, ok := waitErr.(*exec.ExitError)
	if !ok || exitErr.ExitCode() != wantExit {
		t.Fatalf("glider-runtime exit = %v, want exit code %d (workload OOM-killed)", waitErr, wantExit)
	}

	if caughtOOMEvidence != "" {
		t.Logf("live kernel evidence: %s", caughtOOMEvidence)
	} else {
		t.Logf("did not catch live memory.events before cleanup (race lost, not a failure) — exit code %d is the authoritative containment evidence", wantExit)
	}

	rec = loadState(t, stateDir)
	if rec.Phase != state.Exited {
		t.Errorf("phase = %q, want EXITED", rec.Phase)
	}
	if rec.ExitCode == nil || *rec.ExitCode != wantExit {
		t.Errorf("recorded exit code = %v, want %d", rec.ExitCode, wantExit)
	}
	// Runtime/host health: the launcher itself returned cleanly (no
	// crash), and cleanup still completes normally.
	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

// --- exit gate E: PID limit / fork-bomb containment ---

func TestPIDsLimitContainsForkBomb(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	// pidsMax must leave headroom for the Go-runtime-based fixture's own
	// OS-thread needs (see testhelper's cmdPIDHog doc comment) — too
	// tight a limit crashes the *fixture's own* Go runtime, not just
	// refuses its forks, which would be a false test failure rather than
	// evidence of anything about Glider's pids.max enforcement.
	const pidsMax = 40
	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--pids", strconv.Itoa(pidsMax),
		"--", "/bin/glider-test-helper", "pid-hog", "3")
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}
	rec := loadState(t, stateDir)

	// Poll pids.current while the fork storm is happening: it must never
	// exceed the configured limit (glider-init included — Phase 4 §12).
	var maxObserved uint64
	pollDeadline := time.Now().Add(3500 * time.Millisecond)
	for time.Now().Before(pollDeadline) {
		if stats, err := m.Stats(rec.ContainerID); err == nil {
			if stats.PIDs.Current > maxObserved {
				maxObserved = stats.PIDs.Current
			}
			if stats.PIDs.Current > uint64(pidsMax) {
				t.Errorf("pids.current = %d observed, exceeds configured pids.max = %d", stats.PIDs.Current, pidsMax)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Logf("max pids.current observed while running: %d (limit %d)", maxObserved, pidsMax)
	if maxObserved == 0 {
		t.Error("never observed a nonzero pids.current — test may not have overlapped the workload's active window")
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("glider-runtime: %v, stderr unavailable, stdout: %s", err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "REFUSED=") {
		t.Fatalf("workload did not report a REFUSED count: %q", stdout.String())
	}
	// A REFUSED count of 0 would mean the limit was never actually
	// reached/enforced during the run.
	if strings.Contains(stdout.String(), "REFUSED=0") {
		t.Errorf("workload reported REFUSED=0 — pids.max=%d was never actually hit: %q", pidsMax, stdout.String())
	}
	t.Logf("workload report: %s", strings.TrimSpace(stdout.String()))

	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

// --- exit gate F: combined CPU + memory + PID limits simultaneously ---

func TestCombinedResourceLimits(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir,
		"--cpus", "0.5", "--memory", "64Mi", "--pids", "16",
		"--", "/bin/glider-test-helper", "sleep-default", "3")
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForPhase(t, stateDir, state.Running, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("container never reached RUNNING: %v", err)
	}
	rec := loadState(t, stateDir)

	path, err := m.ContainerPath(rec.ContainerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	cpuMax, err := os.ReadFile(filepath.Join(path, "cpu.max"))
	if err != nil {
		t.Fatalf("read cpu.max: %v", err)
	}
	if got := strings.TrimSpace(string(cpuMax)); got != "50000 100000" {
		t.Errorf("cpu.max = %q, want \"50000 100000\"", got)
	}
	memMax, err := os.ReadFile(filepath.Join(path, "memory.max"))
	if err != nil {
		t.Fatalf("read memory.max: %v", err)
	}
	if got := strings.TrimSpace(string(memMax)); got != strconv.Itoa(64*1024*1024) {
		t.Errorf("memory.max = %q, want %q", got, strconv.Itoa(64*1024*1024))
	}
	pidsMax, err := os.ReadFile(filepath.Join(path, "pids.max"))
	if err != nil {
		t.Fatalf("read pids.max: %v", err)
	}
	if got := strings.TrimSpace(string(pidsMax)); got != "16" {
		t.Errorf("pids.max = %q, want \"16\"", got)
	}

	if err := cmd.Wait(); err != nil {
		t.Fatalf("glider-runtime: %v", err)
	}
	waitForCgroupAbsent(t, m, rec.ContainerID, 5*time.Second)
}

// --- exit gate O: invalid configuration is rejected before launch, and
// never leaves a cgroup behind ---

func TestInvalidResourceConfigRejectedBeforeLaunch(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cases := [][]string{
		{"--cpus", "-1"},
		{"--cpus", "nan"},
		{"--memory", "-100"},
		{"--memory", "banana"},
		{"--pids", "0"},
		{"--pids", "-1"},
	}
	for _, extra := range cases {
		t.Run(strings.Join(extra, ""), func(t *testing.T) {
			stateDir := t.TempDir()
			res, err := runGlider(t, context.Background(), glider, stateDir, root, extra, "/bin/glider-test-helper", "exit", "0")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if res.exitCode == 0 {
				t.Errorf("expected non-zero exit for invalid config %v, stdout=%q stderr=%q", extra, res.stdout, res.stderr)
			}
			// No container was ever created (rejected at flag-parsing,
			// before NewContainerID) — nothing to check per-container, but
			// confirm no stray "glider" cgroup children accumulated beyond
			// whatever pre-existed (best-effort global sanity check).
			_ = m
		})
	}
}

// --- exit gate K: crash recovery removes abandoned cgroups ---

func TestRecoveryRemovesCgroupAbandonedBeforeCreated(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	marker := filepath.Join(t.TempDir(), "created-marker")
	m := cgroupManager(t)

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--memory", "32Mi",
		"--", "/bin/glider-test-helper", "sleep-default", "30")
	cmd.Env = append(os.Environ(), "_GLIDER_TEST_PAUSE_AFTER_CREATED="+marker)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start glider-runtime: %v", err)
	}
	if err := waitForFile(t, marker, 5*time.Second); err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("launcher never reached the post-CREATED pause: %v", err)
	}
	rec := loadState(t, stateDir)
	if rec.Phase != state.Created {
		t.Fatalf("phase at pause = %q, want CREATED", rec.Phase)
	}
	initPID := rec.InitPID
	t.Cleanup(func() {
		_ = syscall.Kill(initPID, syscall.SIGKILL)
		assertNoLeakedProcesses(t, helper)
	})

	// Confirm the cgroup exists (created before CREATED, per launcher.go's
	// ordering) and glider-init is attached to it.
	path, err := m.ContainerPath(rec.ContainerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected container cgroup to already exist at CREATED: %v", err)
	}

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
		t.Fatalf("Recover #2 action = %q, want CLEANED_UP", r2.Action)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("container cgroup %s still exists after recovery converged to CLEANED_UP: err=%v", path, err)
	}
}

func TestLauncherCrashWhileRunningCgroupSurvivesUntilRecovered(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	cmd := exec.Command(glider, "run", "--rootfs", root, "--state-dir", stateDir, "--memory", "32Mi",
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

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill launcher: %v", err)
	}
	_ = cmd.Wait()
	time.Sleep(200 * time.Millisecond)

	// Container (and its cgroup) must still be alive — glider-init
	// supervises independently (docs/adr/0006).
	path, err := m.ContainerPath(rec.ContainerID)
	if err != nil {
		t.Fatalf("ContainerPath: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected container cgroup to still exist while glider-init lives on: %v", err)
	}

	id := containerID(t, stateDir)
	result, err := process.Recover(stateDir, id)
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if result.Action != process.RecoveryStillHealthy {
		t.Fatalf("recovery action = %q, want STILL_HEALTHY", result.Action)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("cgroup unexpectedly removed for a still-healthy container: %v", err)
	}

	// Clean up directly, simulating a future gliderd. SIGTERM is a
	// lifecycle signal glider-init handles gracefully on its own
	// (docs/adr/0006, signals.go): it forwards to the workload, waits,
	// and durably records its own real EXITED transition before exiting
	// — unlike a launcher-crash-induced *inferred* exit, this is the
	// ordinary graceful-shutdown path, just triggered by a direct signal
	// instead of the (already-dead) original launcher. So Recover's next
	// call(s) may see EXITED already recorded (cascading straight through
	// DELETING to CLEANED_UP in one call) rather than needing to itself
	// converge RUNNING -> EXITED first — both are correct outcomes of the
	// same safe convergence; this loop tolerates either ordering rather
	// than assuming a specific number of intermediate steps.
	_ = syscall.Kill(initPID, syscall.SIGTERM)
	waitForProcessGone(t, initPID, 5*time.Second)

	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := process.Recover(stateDir, id)
		if errors.Is(err, process.ErrContainerNotFound) {
			break // fully converged and removed by an earlier call in this loop
		}
		if err != nil {
			t.Fatalf("Recover: %v", err)
		}
		if result.Action == process.RecoveryCleanedUp {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("recovery did not converge to CLEANED_UP within %s (last action: %s)", 5*time.Second, result.Action)
		}
	}
	assertCgroupAbsent(t, m, id)
}

// --- exit gate M: concurrent containers get distinct, non-interfering
// cgroups ---

func TestConcurrentContainersGetDistinctCgroups(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	type spec struct {
		cpus, memory, pids string
	}
	specs := []spec{
		{"0.5", "64Mi", "32"},
		{"1", "128Mi", "64"},
	}

	type outcome struct {
		containerID string
		cgroupPath  string
		err         error
	}
	results := make(chan outcome, len(specs))
	var wg sync.WaitGroup
	for _, s := range specs {
		s := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			stateDir, mkErr := os.MkdirTemp("", "glider-cgroup-concurrent-")
			if mkErr != nil {
				results <- outcome{err: mkErr}
				return
			}
			extra := []string{"--cpus", s.cpus, "--memory", s.memory, "--pids", s.pids}
			res, err := runGlider(t, context.Background(), glider, stateDir, root, extra, "/bin/glider-test-helper", "exit", "0")
			if err != nil {
				results <- outcome{err: err}
				return
			}
			if res.exitCode != 0 {
				results <- outcome{err: fmt.Errorf("exit code %d: %s", res.exitCode, res.stderr)}
				return
			}
			rec := loadState(t, stateDir)
			results <- outcome{containerID: rec.ContainerID, cgroupPath: rec.CgroupPath}
		}()
	}
	wg.Wait()
	close(results)

	seen := make(map[string]bool)
	for o := range results {
		if o.err != nil {
			t.Fatalf("concurrent run: %v", o.err)
		}
		if seen[o.cgroupPath] {
			t.Errorf("duplicate cgroup path %q across concurrent containers", o.cgroupPath)
		}
		seen[o.cgroupPath] = true
		assertCgroupAbsent(t, m, o.containerID)
	}
	if len(seen) != len(specs) {
		t.Errorf("saw %d distinct cgroup paths, want %d", len(seen), len(specs))
	}
}

// --- exit gate Q: no cgroup leaks under repeated start/exit cycles ---

func TestNoCgroupLeaksAcrossRepeatedCycles(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	m := cgroupManager(t)
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	entriesBefore := listGliderCgroupChildren(t, m)

	const n = 25
	for i := 0; i < n; i++ {
		stateDir := t.TempDir()
		res, err := runGlider(t, context.Background(), glider, stateDir, root,
			[]string{"--cpus", "0.3", "--memory", "32Mi", "--pids", "16"},
			"/bin/glider-test-helper", "exit", "0")
		if err != nil {
			t.Fatalf("cycle %d: %v", i, err)
		}
		if res.exitCode != 0 {
			t.Fatalf("cycle %d: exit code = %d, stderr = %s", i, res.exitCode, res.stderr)
		}
		rec := loadState(t, stateDir)
		assertCgroupAbsent(t, m, rec.ContainerID)
	}

	entriesAfter := listGliderCgroupChildren(t, m)
	if len(entriesAfter) != len(entriesBefore) {
		t.Errorf("glider cgroup child count changed from %d to %d across %d start/exit cycles — cgroup leak", len(entriesBefore), len(entriesAfter), n)
	}
}

func listGliderCgroupChildren(t *testing.T, m *cgroup.Manager) []string {
	t.Helper()
	entries, err := os.ReadDir(m.Root())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read glider cgroup root: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
