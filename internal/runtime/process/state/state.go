// Package state implements the container lifecycle state machine and its
// on-disk persistence, as specified in docs/design/container-lifecycle.md.
//
// This package is intentionally OS-independent: it only does JSON file I/O.
// The Linux-specific launch machinery (internal/runtime/process) is the sole
// writer, and owns the state record for the lifetime of a Phase 1
// glider-runtime invocation (docs/design/runtime.md §6 — parent-owned state,
// since Phase 1 is a single foreground run, not a daemon).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Phase is a container lifecycle state, per container-lifecycle.md §1.
// Phase 1 exercises Creating -> Created -> Running -> {Exited | Failed}.
// Stopping, Deleting, and Absent are part of the documented lifecycle but
// Absent is represented by the state file simply not existing, and Deleting
// belongs to gliderd's reconciler (Phase 10) — a Phase 1 foreground run has
// no separate deletion step (runtime.md §6 "in scope" stops at EXITED).
type Phase string

const (
	Creating Phase = "CREATING"
	Created  Phase = "CREATED"
	Running  Phase = "RUNNING"
	Stopping Phase = "STOPPING"
	Exited   Phase = "EXITED"
	Failed   Phase = "FAILED"
)

// transitions enumerates the legal edges from container-lifecycle.md §1/§3.
var transitions = map[Phase][]Phase{
	Creating: {Created, Failed},
	Created:  {Running, Failed},
	Running:  {Stopping, Exited, Failed},
	Stopping: {Exited, Failed},
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

// Record is the durable, on-disk representation of one container's state.
type Record struct {
	ContainerID string    `json:"container_id"`
	Phase       Phase     `json:"phase"`
	RootFS      string    `json:"rootfs"`
	Argv        []string  `json:"argv"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`

	// Pid and PidStartTime identify the container's init/workload process
	// (the same OS process throughout Phase 1's direct-exec design, per
	// runtime.md §1/§5). PidStartTime is /proc/<pid>/stat's starttime field
	// — recorded here per container-lifecycle.md §5's PID-reuse defense,
	// even though Phase 1 has no daemon/reconciler to act on a mismatch yet
	// (that verification lands with gliderd in Phase 10).
	Pid          int    `json:"pid,omitempty"`
	PidStartTime uint64 `json:"pid_start_time,omitempty"`

	ExitCode *int   `json:"exit_code,omitempty"`
	Error    string `json:"error,omitempty"`
}

const fileName = "state.json"

// Dir returns the state directory for a given container under stateRoot.
func Dir(stateRoot, containerID string) string {
	return filepath.Join(stateRoot, containerID)
}

// Save durably persists rec to <dir>/state.json.
//
// Per container-lifecycle.md §3 ("the record of intent is durable before
// the resource exists"), the write is fsynced and published via rename so a
// crash never leaves a partially-written state file: readers see either the
// previous complete record or the new complete one, never a torn write.
func Save(dir string, rec Record) error {
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
func Load(dir string) (Record, error) {
	var rec Record
	data, err := os.ReadFile(filepath.Join(dir, fileName))
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("parse state file: %w", err)
	}
	return rec, nil
}
