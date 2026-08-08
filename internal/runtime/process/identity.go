package process

import (
	"fmt"
	"strconv"
	"strings"
)

// ProcessIdentity is the PID-reuse-safe identity tuple for a process, per
// container-lifecycle.md §5: PID + /proc/<pid>/stat's starttime field, a
// monotonic-since-boot value the kernel never reuses for a different
// process at the same PID. Any code that wants to act on a recorded PID
// (signal it, treat it as still-owning a resource) must compare both
// fields, never the PID alone — see ValidateProcessIdentity.
type ProcessIdentity struct {
	PID       int    `json:"pid"`
	StartTime uint64 `json:"start_time"`
}

// IsZero reports whether id is the unset value — no process was ever
// recorded (e.g. a container that never reached CREATED).
func (id ProcessIdentity) IsZero() bool {
	return id.PID == 0 && id.StartTime == 0
}

// statInfo is the subset of /proc/<pid>/stat fields Glider cares about.
type statInfo struct {
	// State is the single-character process state (field 3): 'R' running,
	// 'S' sleeping, 'Z' zombie (exited, not yet reaped by its parent), etc.
	// A zombie is treated as "not a live, functioning process" by
	// ValidateProcessIdentity even though /proc/<pid> still exists for it —
	// see identity_linux.go.
	State     byte
	PPid      int
	StartTime uint64
}

// parseStat extracts the process state (field 3) and starttime (field 22,
// both 1-indexed per proc(5)) from the raw contents of /proc/<pid>/stat.
//
// The comm field (field 2) is parenthesized and may itself contain spaces
// or even parentheses — a process can rename itself via
// prctl(PR_SET_NAME)/argv[0] tricks to nearly anything, including a string
// engineered to confuse a naive parser (e.g. "1234 (evil) S ) 99" ...").
// The kernel's own writer guarantees the *last* ')' on the line closes the
// comm field (everything after it is fixed-format, whitespace-delimited,
// and never itself contains a ')'), so anchoring on the last ')' — not the
// first, and not a strings.Fields split of the whole line — is the only
// reliable way to separate comm from the rest.
func parseStat(data []byte) (statInfo, error) {
	s := string(data)
	i := strings.LastIndexByte(s, ')')
	if i < 0 || i+2 >= len(s) {
		return statInfo{}, fmt.Errorf("unexpected /proc/<pid>/stat format: no closing ')' for comm field")
	}
	fields := strings.Fields(s[i+2:])
	// Fields after comm start at overall field 3 (state); starttime is
	// overall field 22, i.e. index 22-3=19 in this slice. PPid is field 4,
	// index 1.
	const stateIdx = 3 - 3
	const ppidIdx = 4 - 3
	const starttimeIdx = 22 - 3
	if len(fields) <= starttimeIdx {
		return statInfo{}, fmt.Errorf("unexpected /proc/<pid>/stat field count: got %d fields after comm, need more than %d", len(fields), starttimeIdx)
	}
	if len(fields[stateIdx]) != 1 {
		return statInfo{}, fmt.Errorf("unexpected /proc/<pid>/stat state field: %q", fields[stateIdx])
	}
	ppid, err := strconv.Atoi(fields[ppidIdx])
	if err != nil {
		return statInfo{}, fmt.Errorf("parse ppid field: %w", err)
	}
	st, err := strconv.ParseUint(fields[starttimeIdx], 10, 64)
	if err != nil {
		return statInfo{}, fmt.Errorf("parse starttime field: %w", err)
	}
	return statInfo{State: fields[stateIdx][0], PPid: ppid, StartTime: st}, nil
}
