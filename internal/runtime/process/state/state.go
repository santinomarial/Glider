// Package state implements the container lifecycle state machine and its
// on-disk persistence, as specified in docs/design/container-lifecycle.md.
//
// This package is intentionally OS-independent: it only does JSON file I/O.
// The Linux-specific launch machinery (internal/runtime/process) is the
// writer for most transitions; as of Phase 2, glider-init itself also
// writes the terminal EXITED transition directly (docs/adr/0006), so this
// package's on-disk format is a durable contract between two writing
// processes, not just internal to one — see SchemaVersion.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrCorruptState means the on-disk state file exists but is not a valid
// record of the current schema (truncated, invalid JSON, missing a
// required field, or an unrecognized phase value) — distinct from
// ErrUnsupportedVersion (a validly-structured record from a version this
// binary doesn't understand) and distinct from ordinary process death
// (container-lifecycle.md §3: "state corruption" vs. "normal process
// death" must never be conflated — corruption must fail safely, never
// drive a guess about which process to signal/kill).
var ErrCorruptState = errors.New("corrupt container state")

// ErrUnsupportedVersion means the on-disk state file's schema_version does
// not match SchemaVersion.
var ErrUnsupportedVersion = errors.New("unsupported container state schema version")

// SchemaVersion is the current on-disk state record format. No migration
// infrastructure is implemented (not needed yet — nothing has shipped
// that requires reading an older format); Load rejects any file whose
// version doesn't match exactly, so a mismatch is a clear, loud error
// rather than a silent misinterpretation of fields that changed meaning
// (e.g. Phase 1's "Pid" was the workload's own PID; Phase 2's "InitPID"
// is not the same thing; Phase 4 adds cgroup identity — see
// container-lifecycle.md and docs/design/cgroups.md).
const SchemaVersion = 4

// Phase is a container lifecycle state, per container-lifecycle.md §1.
type Phase string

const (
	Creating Phase = "CREATING"
	Created  Phase = "CREATED"
	Running  Phase = "RUNNING"
	Stopping Phase = "STOPPING"
	Exited   Phase = "EXITED"
	Failed   Phase = "FAILED"
	Deleting Phase = "DELETING"
)

// transitions enumerates the legal edges from container-lifecycle.md §1/§3.
// Deleting has no outgoing edge here: convergence to ABSENT is represented
// by removing the state file entirely (Dir's caller does that once cleanup
// is confirmed complete), not by a further Phase value.
var transitions = map[Phase][]Phase{
	Creating: {Created, Failed},
	Created:  {Running, Failed},
	Running:  {Stopping, Exited, Failed},
	Stopping: {Exited, Failed},
	Exited:   {Deleting},
	Failed:   {Deleting},
}

// ValidTransition reports whether moving from `from` to `to` is a legal
// container-lifecycle.md transition. The zero Phase ("") represents ABSENT,
// which may only transition to Creating.
func ValidTransition(from, to Phase) bool {
	if from == "" {
		return to == Creating
	}
	for _, next := range transitions[from] {
		if next == to {
			return true
		}
	}
	return false
}

// Resources mirrors cgroup.Resources' shape as a plain, dependency-free
// value type — this package stays OS-independent (its own package doc
// comment) and does not import the Linux-only cgroup package merely to
// borrow a struct shape. cgroup.Resources is the single source of truth
// for what these fields *mean* (units, "<=0 means unlimited" convention,
// validation); this is only its durable, serializable shadow.
type Resources struct {
	CPUCores    float64 `json:"cpu_cores,omitempty"`
	MemoryBytes int64   `json:"memory_bytes,omitempty"`
	PIDsMax     int64   `json:"pids_max,omitempty"`
}

// Record is the durable, on-disk representation of one container's state.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	ContainerID   string `json:"container_id"`
	Phase         Phase  `json:"phase"`

	RootFS     string   `json:"rootfs"`
	Argv       []string `json:"argv"`
	Hostname   string   `json:"hostname,omitempty"`
	Env        []string `json:"env,omitempty"`
	WorkingDir string   `json:"working_dir,omitempty"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`

	// InitPID/InitStartTime identify glider-init — the namespace's PID 1,
	// which remains the container's durable owner for its entire life
	// (docs/adr/0006-glider-init-pid1-supervisor.md). This is the identity
	// checked by recovery (container-lifecycle.md §5 PID-reuse defense):
	// because glider-init is the namespace's PID 1, the kernel guarantees
	// that if this process is gone, every other process in the namespace
	// (including the workload) is gone too — see the ADR for why this
	// makes it sufficient to check only this one identity, not a process
	// tree. Recorded at CREATED, by the launcher (the only process that
	// ever observes glider-init's host-visible PID directly).
	InitPID       int    `json:"init_pid,omitempty"`
	InitStartTime uint64 `json:"init_start_time,omitempty"`

	// WorkloadPID/WorkloadStartTime identify the workload process itself,
	// recorded best-effort at RUNNING for observability/debugging. Not
	// load-bearing for recovery correctness (InitPID is) — resolving a
	// child's host-visible PID from outside its PID namespace has no
	// syscall-guaranteed path (see runtime.md), so this is populated on a
	// best-effort basis and its absence is not an error.
	WorkloadPID       int    `json:"workload_pid,omitempty"`
	WorkloadStartTime uint64 `json:"workload_start_time,omitempty"`

	// CgroupPath is the container's cgroup v2 path, relative to the
	// discovered cgroup2 mount point (e.g. "glider/<container-id>") —
	// Phase 4, docs/design/cgroups.md "Cgroup identity in runtime state".
	// Recorded for observability/auditability; recovery and cleanup
	// re-derive this path deterministically from ContainerID rather than
	// trusting this field blindly (container-lifecycle.md's "do not treat
	// the state file as proof" principle), since it is always
	// reconstructible and doing so is more robust against a stale or
	// missing value than trusting a persisted copy.
	CgroupPath string `json:"cgroup_path,omitempty"`

	// Resources is the resource limits requested for this container
	// (Phase 4). The zero value means no limits were requested (every
	// controller left at "max" — see cgroup.Resources's doc comment).
	Resources Resources `json:"resources,omitempty"`

	ExitCode *int `json:"exit_code,omitempty"`

	// ExitedInferred is true when the EXITED transition was reached by
	// recovery concluding glider-init is gone (§5 identity check failed),
	// rather than by directly observing the workload's wait status. In
	// that case ExitCode is not a real observed value — see
	// container-lifecycle.md §4.
	ExitedInferred bool `json:"exited_inferred,omitempty"`

	Error string `json:"error,omitempty"`
}

const fileName = "state.json"
const lockFileName = "lock"

// Dir returns the state directory for a given container under stateRoot.
func Dir(stateRoot, containerID string) string {
	return filepath.Join(stateRoot, containerID)
}

// LockPath returns the per-container advisory lock file path used by
// Lock (lock.go) to serialize concurrent lifecycle operations against the
// same container (docs/adr/0006, container-lifecycle.md §7 concurrency).
func LockPath(dir string) string {
	return filepath.Join(dir, lockFileName)
}

// Save durably persists rec to <dir>/state.json, stamping SchemaVersion.
//
// Per container-lifecycle.md §3 ("the record of intent is durable before
// the resource exists"), the write is fsynced and published via rename so a
// crash never leaves a partially-written state file: readers see either the
// previous complete record or the new complete one, never a torn write.
func Save(dir string, rec Record) error {
	rec.SchemaVersion = SchemaVersion

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create state dir %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state record: %w", err)
	}

	final := filepath.Join(dir, fileName)
	tmp := final + ".tmp"

	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp state file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("publish state file: %w", err)
	}

	// Best-effort directory fsync so the rename itself survives a crash
	// immediately after this call returns, not just the file contents.
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}

	return nil
}

// Load reads the state record for a container. A missing file means ABSENT
// and is reported as a plain *os.PathError so callers can use os.IsNotExist.
//
// A record whose SchemaVersion doesn't match the version this binary
// understands is rejected with a distinguishable error (ErrUnsupportedVersion
// wrapped in) rather than partially/incorrectly interpreted — see
// SchemaVersion's doc comment.
func Load(dir string) (Record, error) {
	var rec Record
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("%w: parse state file: %v", ErrCorruptState, err)
	}
	if rec.SchemaVersion != SchemaVersion {
		return rec, fmt.Errorf("%w: state file schema_version=%d, this binary supports %d",
			ErrUnsupportedVersion, rec.SchemaVersion, SchemaVersion)
	}
	if rec.ContainerID == "" {
		return rec, fmt.Errorf("%w: missing required field container_id", ErrCorruptState)
	}
	if !isKnownPhase(rec.Phase) {
		return rec, fmt.Errorf("%w: unknown phase %q", ErrCorruptState, rec.Phase)
	}
	return rec, nil
}

func isKnownPhase(p Phase) bool {
	switch p {
	case Creating, Created, Running, Stopping, Exited, Failed, Deleting:
		return true
	default:
		return false
	}
}
