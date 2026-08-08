//go:build linux

package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// gliderRootName is the fixed child name Glider creates under the host's
// cgroup v2 mount point. It is deliberately NOT derived from the
// launcher's own ambient cgroup (e.g. "wherever this process happens to
// be running") — that would make the resulting path unstable across
// invocations with different starting cgroups (an operator's interactive
// shell vs. a later `recover` invocation from a different context), which
// would break recovery's ability to re-derive the same path deterministically
// from a container ID alone (docs/design/cgroups.md "Naming").
const gliderRootName = "glider"

// bootstrapLeaf is the well-known child cgroup Glider's own process(es)
// live in, so that neither the true cgroup root nor Glider's own root
// ("glider/") ever has a direct member process — required before either
// can enable subtree_control for its children (cgroup v2's "no internal
// process" constraint, empirically confirmed against this project's
// Linux test environment — docs/design/cgroups.md "Delegation"). Multiple
// concurrent glider-runtime processes safely share this same leaf; it is
// intentionally never removed (analogous to systemd's own "init.scope"
// convention for a unit that both runs a process and manages children).
const bootstrapLeaf = "_supervisor"

// enabledControllers is the fixed set Phase 4 enables — cpu, memory, and
// pids only. io is deliberately not enabled: it is not configured or
// tested this phase (docs/design/cgroups.md "I/O — deferred").
var enabledControllers = []string{"cpu", "memory", "pids"}

// Manager owns one host's Glider cgroup v2 subtree.
type Manager struct {
	mountPoint string // e.g. "/sys/fs/cgroup" (discovered, not assumed)
	root       string // mountPoint + "/" + gliderRootName
}

// NewManager discovers the host's cgroup v2 mount point and returns a
// Manager for Glider's subtree under it. It does not itself require or
// perform delegation — call EnsureDelegated for that — so that discovery
// (needed even for read-only operations like Stats) doesn't have the
// side effects delegation does.
func NewManager() (*Manager, error) {
	mp, err := findMountPoint()
	if err != nil {
		return nil, err
	}
	if err := hasUnifiedControllers(mp); err != nil {
		return nil, err
	}
	return &Manager{mountPoint: mp, root: filepath.Join(mp, gliderRootName)}, nil
}

// Root returns Glider's cgroup subtree root (e.g. "/sys/fs/cgroup/glider").
func (m *Manager) Root() string { return m.root }

// EnsureDelegated bootstraps Glider's cgroup subtree so cpu/memory/pids
// are available to per-container children, idempotently — see
// docs/design/cgroups.md "Delegation" for the full sequence and why it's
// shaped this way. Safe to call on every launch: the common case after
// the first successful call on a host is a single cheap read.
func (m *Manager) EnsureDelegated() error {
	if ok, _ := hasSubtreeControllers(m.root, enabledControllers); ok {
		return nil
	}

	leaf := filepath.Join(m.root, bootstrapLeaf)
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		return fmt.Errorf("create glider cgroup bootstrap leaf: %w", err)
	}

	// Move only THIS process into the bootstrap leaf — never anything
	// else (Phase 4 §28: "do not work around delegation failures with
	// unsafe host modifications"). This vacates both the true cgroup root
	// and Glider's own root of direct member processes in one atomic
	// write, which is the precondition for enabling subtree_control on
	// either.
	if err := writeFile(filepath.Join(leaf, "cgroup.procs"), strconv.Itoa(os.Getpid())); err != nil {
		return fmt.Errorf("move into glider cgroup bootstrap leaf: %w", err)
	}

	if err := enableControllers(m.mountPoint, enabledControllers); err != nil {
		return fmt.Errorf("%w: enable controllers at cgroup root: %v", ErrNotDelegated, err)
	}
	if err := enableControllers(m.root, enabledControllers); err != nil {
		return fmt.Errorf("%w: enable controllers at glider cgroup root: %v", ErrNotDelegated, err)
	}
	return nil
}

// ContainerPathRelative returns containerID's cgroup path relative to the
// discovered cgroup2 mount point (e.g. "glider/<id>") — what
// state.Record.CgroupPath stores (Phase 4 §15: "do not persist arbitrary
// absolute paths if a structured relative identity is safer").
func (m *Manager) ContainerPathRelative(containerID string) (string, error) {
	if err := validateContainerID(containerID); err != nil {
		return "", err
	}
	return gliderRootName + "/" + containerID, nil
}

// ContainerPath returns the absolute cgroup path for containerID,
// validating the ID first (never accepted as a literal path component
// without validation — Phase 4 §42).
func (m *Manager) ContainerPath(containerID string) (string, error) {
	if err := validateContainerID(containerID); err != nil {
		return "", err
	}
	return filepath.Join(m.root, containerID), nil
}

// Create makes containerID's cgroup and durably configures its resource
// limits — but does NOT attach any process (see Attach). Callers must
// call Create, then Attach, never the reverse: the critical Phase 4
// invariant ("a workload must not run before limits are established") is
// enforced by this ordering, not by anything inside Create itself. If any
// configuration write fails, the partially-created cgroup is removed
// before returning the error — Create never leaves a half-configured
// cgroup behind.
func (m *Manager) Create(containerID string, res Resources) (path string, err error) {
	path, err = m.ContainerPath(containerID)
	if err != nil {
		return "", err
	}

	if err := os.Mkdir(path, 0o755); err != nil && !os.IsExist(err) {
		return "", fmt.Errorf("create container cgroup: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(path)
		}
	}()

	if err = writeFile(filepath.Join(path, "cpu.max"), cpuMaxValue(res.CPUCores)); err != nil {
		return "", fmt.Errorf("configure cpu.max: %w", err)
	}
	if err = writeFile(filepath.Join(path, "memory.max"), memoryMaxValue(res.MemoryBytes)); err != nil {
		return "", fmt.Errorf("configure memory.max: %w", err)
	}
	if err = writeFile(filepath.Join(path, "pids.max"), pidsMaxValue(res.PIDsMax)); err != nil {
		return "", fmt.Errorf("configure pids.max: %w", err)
	}
	return path, nil
}

// Attach adds pid (a host-visible PID) to containerID's cgroup. Called
// exactly once per container, for glider-init's own host PID — every
// descendant (the workload, its own children, ...) inherits cgroup
// membership automatically on fork, needing no separate attachment
// (Phase 4 §14).
func (m *Manager) Attach(containerID string, pid int) error {
	path, err := m.ContainerPath(containerID)
	if err != nil {
		return err
	}
	if err := writeFile(filepath.Join(path, "cgroup.procs"), strconv.Itoa(pid)); err != nil {
		return fmt.Errorf("attach pid %d to container cgroup: %w", pid, err)
	}
	return nil
}

// VerifyAttached reports whether pid's real, kernel-reported cgroup
// membership (/proc/<pid>/cgroup, read from the caller's own cgroup
// namespace vantage point) is actually containerID's cgroup — Phase 4
// §14's "verify attachment" step, checked against kernel evidence rather
// than trusted purely because Attach's write returned success.
func (m *Manager) VerifyAttached(containerID string, pid int) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cgroup", pid))
	if err != nil {
		return false, fmt.Errorf("read /proc/%d/cgroup: %w", pid, err)
	}
	line := strings.TrimSpace(string(data))
	idx := strings.LastIndex(line, ":")
	if idx < 0 {
		return false, fmt.Errorf("unexpected /proc/%d/cgroup format: %q", pid, line)
	}
	relPath := line[idx+1:]
	wantSuffix := "/" + gliderRootName + "/" + containerID
	return strings.HasSuffix(relPath, wantSuffix), nil
}

// Remove idempotently removes containerID's cgroup: "already gone" is
// success, not error (container-lifecycle.md §6). Returns ErrPopulated if
// the cgroup still has live member processes — callers must ensure the
// owning process tree is gone first (WaitUnpopulated), removal is never
// forced here.
func (m *Manager) Remove(containerID string) error {
	path, err := m.ContainerPath(containerID)
	if err != nil {
		return err
	}

	populated, err := isPopulated(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("check cgroup population before removal: %w", err)
	}
	if populated {
		return fmt.Errorf("%w: %s", ErrPopulated, path)
	}

	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("remove container cgroup: %w", err)
	}
	return nil
}

// WaitUnpopulated polls (bounded, backing off — not a fixed sleep-and-hope
// guess) containerID's cgroup.events "populated" field until it reads 0
// or timeout elapses. This is real kernel-observed state, not a
// heuristic: cgroup v2 updates cgroup.events synchronously with process
// exit/reap, so "populated 0" is authoritative, unlike polling /proc for
// absence of a specific PID (which can race PID reuse — this instead asks
// the cgroup itself).
func (m *Manager) WaitUnpopulated(containerID string, timeout time.Duration) error {
	path, err := m.ContainerPath(containerID)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(timeout)
	backoff := time.Millisecond
	for {
		populated, err := isPopulated(path)
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return fmt.Errorf("check cgroup population: %w", err)
		}
		if !populated {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %s did not become unpopulated within %s", ErrPopulated, path, timeout)
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

func isPopulated(cgroupPath string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(cgroupPath, "cgroup.events"))
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "populated" {
			return fields[1] == "1", nil
		}
	}
	return false, fmt.Errorf("cgroup.events at %s: missing populated field", cgroupPath)
}

// hasSubtreeControllers reports whether path's cgroup.subtree_control
// already lists every controller in want.
func hasSubtreeControllers(path string, want []string) (bool, error) {
	data, err := os.ReadFile(filepath.Join(path, "cgroup.subtree_control"))
	if err != nil {
		return false, err
	}
	have := make(map[string]bool)
	for _, f := range strings.Fields(string(data)) {
		have[f] = true
	}
	for _, w := range want {
		if !have[w] {
			return false, nil
		}
	}
	return true, nil
}

// enableControllers writes "+<controller>" for each of controllers to
// path's cgroup.subtree_control, making them available to path's
// children. Idempotent: re-enabling an already-enabled controller is a
// kernel no-op success.
func enableControllers(path string, controllers []string) error {
	parts := make([]string, len(controllers))
	for i, c := range controllers {
		parts[i] = "+" + c
	}
	return writeFile(filepath.Join(path, "cgroup.subtree_control"), strings.Join(parts, " "))
}

// writeFile writes value to a cgroup control file, wrapping the error
// with the path for a clearer diagnostic than a bare syscall error would
// give — these are privileged kernel control interfaces (Phase 4 §42),
// and every failure here is something an operator needs to be able to
// locate quickly.
func writeFile(path, value string) error {
	if err := os.WriteFile(path, []byte(value), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}
