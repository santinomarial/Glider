//go:build linux

package cgroup

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Stats is a container cgroup's resource usage, read fresh on every call
// (Phase 4 §23) — a minimal, typed foundation for a future `glider stats`,
// not a telemetry system. Every quantity is a raw, unstructured value the
// kernel provides directly; the caller is not required to already be a
// cgroup expert to know what fields exist, but is expected to know what
// they mean.
type Stats struct {
	CPU    CPUStats
	Memory MemoryStats
	PIDs   PIDsStats
}

// CPUStats mirrors the subset of cpu.stat Phase 4 exposes.
type CPUStats struct {
	UsageUsec     uint64 // total CPU time consumed
	UserUsec      uint64
	SystemUsec    uint64
	NrPeriods     uint64 // number of enforcement periods elapsed
	NrThrottled   uint64 // number of periods the group was throttled in
	ThrottledUsec uint64 // total time spent throttled
}

// MemoryStats mirrors memory.current/memory.peak/memory.events.
type MemoryStats struct {
	CurrentBytes uint64
	// PeakBytes is 0 if the kernel doesn't expose memory.peak (an
	// optional file — Phase 4 §25 requires not assuming every kernel
	// exposes every optional statistic).
	PeakBytes uint64
	Events    MemoryEvents
}

// MemoryEvents mirrors the counters in memory.events relevant to
// diagnosing containment/OOM behavior (Phase 4 §11).
type MemoryEvents struct {
	Low     uint64
	High    uint64 // memory.high breaches (throttling) — Phase 4 doesn't set memory.high itself, but another actor in a shared host could
	Max     uint64 // memory.max breaches
	OOM     uint64 // OOM invocations
	OOMKill uint64 // processes actually killed by the OOM killer
}

// PIDsStats mirrors pids.current/pids.events.
type PIDsStats struct {
	Current uint64
	Events  PIDsEvents
}

// PIDsEvents mirrors pids.events — Max counts how many times a fork was
// refused because pids.max was already reached (Phase 4 §26's fork-bomb
// containment evidence).
type PIDsEvents struct {
	Max uint64
}

// Stats reads containerID's current resource usage from its cgroup.
func (m *Manager) Stats(containerID string) (Stats, error) {
	path, err := m.ContainerPath(containerID)
	if err != nil {
		return Stats{}, err
	}

	var s Stats

	cpuStat, err := readKeyValueFile(filepath.Join(path, "cpu.stat"))
	if err != nil {
		return Stats{}, fmt.Errorf("read cpu.stat: %w", err)
	}
	s.CPU = CPUStats{
		UsageUsec:     cpuStat["usage_usec"],
		UserUsec:      cpuStat["user_usec"],
		SystemUsec:    cpuStat["system_usec"],
		NrPeriods:     cpuStat["nr_periods"],
		NrThrottled:   cpuStat["nr_throttled"],
		ThrottledUsec: cpuStat["throttled_usec"],
	}

	current, err := readUintFile(filepath.Join(path, "memory.current"))
	if err != nil {
		return Stats{}, fmt.Errorf("read memory.current: %w", err)
	}
	s.Memory.CurrentBytes = current

	// memory.peak is optional (kernel/config dependent) — absence is not
	// an error, just reported as 0 (doc comment above).
	if peak, err := readUintFile(filepath.Join(path, "memory.peak")); err == nil {
		s.Memory.PeakBytes = peak
	} else if !os.IsNotExist(err) {
		return Stats{}, fmt.Errorf("read memory.peak: %w", err)
	}

	memEvents, err := readKeyValueFile(filepath.Join(path, "memory.events"))
	if err != nil {
		return Stats{}, fmt.Errorf("read memory.events: %w", err)
	}
	s.Memory.Events = MemoryEvents{
		Low:     memEvents["low"],
		High:    memEvents["high"],
		Max:     memEvents["max"],
		OOM:     memEvents["oom"],
		OOMKill: memEvents["oom_kill"],
	}

	pidsCurrent, err := readUintFile(filepath.Join(path, "pids.current"))
	if err != nil {
		return Stats{}, fmt.Errorf("read pids.current: %w", err)
	}
	s.PIDs.Current = pidsCurrent

	pidsEvents, err := readKeyValueFile(filepath.Join(path, "pids.events"))
	if err != nil {
		return Stats{}, fmt.Errorf("read pids.events: %w", err)
	}
	s.PIDs.Events = PIDsEvents{Max: pidsEvents["max"]}

	return s, nil
}

// readUintFile reads a cgroup control file containing a single value:
// either a decimal integer, or the literal "max" (reported as
// math.MaxUint64's... no — reported as 0, since "unlimited" has no
// meaningful current-usage numeric reading; callers reading a *.current
// file never see "max" in practice, this handles it defensively only).
func readUintFile(path string) (uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if s == "max" {
		return 0, nil
	}
	n, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s: unexpected value %q: %w", path, s, err)
	}
	return n, nil
}

// readKeyValueFile parses a cgroup "key value\n" statistics file (e.g.
// cpu.stat, memory.events, pids.events). Unknown keys are tolerated
// (Phase 4 §24 "the parser should tolerate kernel-added fields it does
// not understand") — the returned map simply carries whatever was
// present; callers look up only the fields they know about. A KNOWN
// line's value failing to parse as an unsigned integer is treated as
// real corruption and returned as an error, not silently skipped — this
// does not depend on line ordering.
func readKeyValueFile(path string) (map[string]uint64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	result := make(map[string]uint64)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return nil, fmt.Errorf("parse %s: unexpected line %q", path, line)
		}
		n, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse %s: field %q: unexpected value %q: %w", path, fields[0], fields[1], err)
		}
		result[fields[0]] = n
	}
	return result, nil
}
