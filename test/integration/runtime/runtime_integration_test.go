//go:build linux

// Package integration exercises the Phase 1 exit gate defined in
// docs/design/runtime.md §6, black-box: it builds the real glider-runtime
// and glider-test-helper binaries and runs them exactly as an operator
// would, rather than calling internal package functions directly. It
// requires root (or equivalent CAP_SYS_ADMIN-class privilege) and a Linux
// kernel with cgroup v2 — see requireRoot/requireCgroupV2 below and
// runtime.md §6 "test environment".
package integration

import (
	"bytes"
	"context"
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

	"github.com/santinomarial/glider/internal/runtime/process/state"
)

var (
	buildOnce sync.Once
	buildErr  error
	gliderBin string
	helperBin string
)

// buildBinaries compiles the actual glider-runtime CLI and the test-helper
// fixture from source once per test run (runtime.md's Phase 1 prompt §15:
// "do not rely on an arbitrary developer machine's /bin/sh ... use a
// reproducible test method"). Both are plain Go builds, not part of the
// state under test, so building them isn't itself part of any timing
// assertion below.
func buildBinaries(t *testing.T) (glider, helper string) {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "glider-integration-bin-")
		if err != nil {
			buildErr = err
			return
		}

		gliderBin = filepath.Join(dir, "glider-runtime")
		out, err := exec.Command("go", "build", "-o", gliderBin, "../../../cmd/glider-runtime").CombinedOutput()
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
// Everything else Phase 1 needs (/proc, /sys, /dev, ...) is created by
// glider-runtime itself (mount_linux.go), matching runtime.md §6's "in
// scope" list — the fixture only supplies what an operator would.
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
func runGlider(t *testing.T, ctx context.Context, glider, stateDir, rootfs string, workload ...string) (runResult, error) {
	t.Helper()
	args := []string{"run", "--rootfs", rootfs, "--hostname", "glider-test", "--state-dir", stateDir, "--"}
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

// assertNoLeakedProcesses fails the test if any process on the host still
// has cmdline referencing bin, satisfying the "cleanup leaves no
// unexpected child process" requirement (runtime.md §6 exit-gate context;
// Phase 1 prompt §20 item 10). A short grace period tolerates the kernel's
// ordinary asynchronous zombie-reap timing.
func assertNoLeakedProcesses(t *testing.T, bin string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
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

// --- exit gate (a): PID namespace isolation, and PID 1 behavior (§9) ---

func TestPIDNamespaceIsolation(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "pid")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "1" {
		t.Errorf("container-observed pid = %q, want \"1\" (own init should be PID 1 inside its namespace)", got)
	}

	rec := loadState(t, stateDir)
	if rec.Pid == 1 {
		t.Errorf("host-observed pid recorded as 1 — expected the launcher's (host-namespace) view to differ from the container's own PID 1 view")
	}
	if rec.Phase != state.Exited {
		t.Errorf("final phase = %q, want EXITED", rec.Phase)
	}
	if rec.ExitCode == nil || *rec.ExitCode != 0 {
		t.Errorf("exit code recorded = %v, want 0", rec.ExitCode)
	}
}

// --- exit gate (b): /proc inside shows only the container's own tree ---

func TestProcShowsOnlyContainerProcessTree(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "procs")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "1" {
		t.Errorf("/proc inside container listed pids %q, want only \"1\" (no host processes visible)", got)
	}
}

// UTS isolation (Phase 1 prompt §8): hostname change inside must not leak
// to the host.

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

	res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "hostname")
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

	// A file that exists on the real host root, outside the fixture rootfs
	// entirely, must not be visible from inside the container — this is
	// the actual proof that pivot_root replaced "/", not merely that a
	// subdirectory was bind-mounted somewhere.
	hostCanary := "/glider-host-canary-" + t.Name()
	if err := os.WriteFile(hostCanary, []byte("host-only"), 0o644); err != nil {
		t.Fatalf("create host canary: %v", err)
	}
	t.Cleanup(func() { os.Remove(hostCanary) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "stat", hostCanary)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.exitCode != 0 {
		t.Fatalf("exit code = %d, stderr = %s", res.exitCode, res.stderr)
	}
	if got := strings.TrimSpace(res.stdout); got != "MISSING" {
		t.Errorf("host-only path visible inside container: stat reported %q, want MISSING", got)
	}

	// Conversely, a file the container writes under its own root lands in
	// the fixture directory on the host side — Phase 1 bind-mounts the
	// operator-supplied --rootfs directly (no OverlayFS until Phase 7,
	// ADR-0004), so this is expected, not a contradiction: it confirms the
	// container's root and the operator-supplied directory are the same
	// filesystem object, while the canary check above confirms the
	// container's root and the *actual host OS root* are not.
	res, err = runGlider(t, context.Background(), glider, stateDir+"-2", root, "/bin/glider-test-helper", "write", "/inside-marker", "hello")
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

// --- exit gate (e): exit status propagation (Phase 1 prompt §12) ---

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
			res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "exit", strconv.Itoa(code))
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
		})
	}
}

// --- exit gate (d): SIGTERM reaches and terminates the workload ---
//
// Per runtime.md §5 and launcher.go's documented caveat, this uses a
// fixture that explicitly traps SIGTERM — an unhandled SIGTERM sent to a
// namespace's PID 1 is not delivered with default disposition by the
// kernel, so asserting default-disposition termination for an arbitrary
// workload would be testing a guarantee Glider does not make in Phase 1.
// What this test proves: the launcher correctly delivers the signal to the
// right process, across the namespace boundary, well within the
// SIGKILL-escalation grace period.

func TestSIGTERMReachesWorkload(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	// The marker path is written by the workload at /marker, i.e. inside
	// the container's own root — which, per the isolation proven above, is
	// the same underlying directory as the fixture rootfs on the host
	// side (no OverlayFS until Phase 7), so it shows up at root/marker.
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
		t.Errorf("SIGTERM handling took %s — suspiciously close to/over the SIGKILL escalation grace period, suggests the signal wasn't delivered promptly", elapsed)
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

// --- exit gate (f): concurrent runs against the same --rootfs don't
// corrupt or interfere with each other ---

func TestConcurrentRunsDoNotInterfere(t *testing.T) {
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
			res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/glider-test-helper", "procs")
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
		if got := strings.TrimSpace(o.res.stdout); got != "1" {
			t.Errorf("run %d: container-observed pids = %q, want \"1\" (each run must get its own isolated PID namespace)", i, got)
		}
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

// --- invalid workload returns a clear error (Phase 1 prompt §20 item 8) ---

func TestInvalidWorkloadReturnsClearError(t *testing.T) {
	requireRoot(t)
	glider, helper := buildBinaries(t)
	root := newRootFS(t, helper)
	stateDir := t.TempDir()
	t.Cleanup(func() { assertNoLeakedProcesses(t, helper) })

	res, err := runGlider(t, context.Background(), glider, stateDir, root, "/bin/does-not-exist")
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
