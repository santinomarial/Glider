//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// CaptureProcessIdentity reads the PID-reuse-safe identity tuple (§
// identity.go) for a currently-live process from /proc. Callers durably
// persist the result at the moment they have positive evidence the process
// exists (e.g. immediately after a successful clone/fork), never
// reconstruct one later from a bare PID.
func CaptureProcessIdentity(pid int) (ProcessIdentity, error) {
	info, err := readStat(pid)
	if err != nil {
		return ProcessIdentity{}, err
	}
	return ProcessIdentity{PID: pid, StartTime: info.StartTime}, nil
}

func readStat(pid int) (statInfo, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return statInfo{}, err
	}
	info, err := parseStat(data)
	if err != nil {
		return statInfo{}, fmt.Errorf("parse /proc/%d/stat: %w", pid, err)
	}
	return info, nil
}

// ValidateProcessIdentity reports whether id still refers to a live,
// functioning process: /proc/<pid> must exist, its current start-time must
// match the recorded one (container-lifecycle.md §5's PID-reuse defense),
// and it must not be a zombie (exited, awaiting reap by its parent — a
// zombie is not a process anyone should signal or treat as still owning
// resources, even though /proc/<pid> still resolves for it). A mismatch,
// absence, or zombie state means "dead/stale", never an error to propagate
// as ambiguous — the one exception is a genuine I/O error reading /proc
// unrelated to the process being gone (e.g. /proc itself unmounted), which
// is returned as an error since it says nothing about the process's actual
// liveness.
func ValidateProcessIdentity(id ProcessIdentity) (bool, error) {
	if id.PID <= 0 {
		return false, nil
	}
	info, err := readStat(id.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("validate process identity for pid %d: %w", id.PID, err)
	}
	if info.State == 'Z' {
		return false, nil
	}
	return info.StartTime == id.StartTime, nil
}

// resolveChildIdentity finds the identity of the (assumed unique)
// host-visible process whose parent is parentPID, by scanning /proc — used
// to best-effort resolve the workload's host PID from the launcher's side
// (launcher.go), since a process cannot learn another process's
// host-visible PID from inside a nested PID namespace by any syscall (that
// boundary is deliberate, not a missing API). glider-init only ever forks
// exactly one direct child (the workload) in Phase 2's architecture, so
// "the child of glider-init's host PID" is unambiguous when it exists.
//
// This is best-effort observability, not a correctness mechanism: callers
// must treat "not found" (ok=false) as normal, not an error — the process
// may not have been visible in this particular scan (e.g. read during a
// brief window), and no recovery/lifecycle decision may depend on it
// (state.Record.WorkloadPID's doc comment).
func resolveChildIdentity(parentPID int) (ProcessIdentity, bool) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return ProcessIdentity{}, false
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		info, err := readStat(pid)
		if err != nil || info.PPid != parentPID {
			continue
		}
		return ProcessIdentity{PID: pid, StartTime: info.StartTime}, true
	}
	return ProcessIdentity{}, false
}
