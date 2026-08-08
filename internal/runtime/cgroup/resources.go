//go:build linux

package cgroup

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Resources is a container's requested resource limits. The zero value
// means "no limits requested" for every field — Glider still writes
// explicit "max" to every controller's limit file in that case (Phase 4
// §34 "prefer explicit semantics") rather than relying on whatever the
// parent cgroup happens to leave inherited.
type Resources struct {
	// CPUCores is a fractional CPU bandwidth limit (e.g. 0.5 = half a
	// core, 2.5 = two and a half cores). <= 0 means unlimited.
	CPUCores float64
	// MemoryBytes is a hard memory limit in bytes. <= 0 means unlimited.
	MemoryBytes int64
	// PIDsMax is the maximum number of tasks (processes/threads) allowed
	// in the container's cgroup, INCLUDING glider-init itself (Phase 4
	// §12 — glider-init consumes one slot of whatever limit is
	// configured; this is not hidden from the accounting). <= 0 means
	// unlimited.
	PIDsMax int64
}

// cpuPeriodUsec is the fixed cpu.max period Glider uses, in microseconds:
// 100ms, matching Docker's and Kubernetes' own convention for this value.
// A fixed period (rather than a configurable one) keeps the CPU model to
// a single understandable knob (--cpus) — deviating from the 100ms
// convention would make Glider's fractional-CPU numbers behave
// differently from what operators coming from those tools already expect,
// for no corresponding benefit at this phase's scope.
const cpuPeriodUsec = 100000

// maxCPUCores is a sanity ceiling against pathological/overflow input
// (Phase 4 §7 "reject ... overflow"), not a real architectural limit —
// generous enough that no plausible real host exceeds it.
const maxCPUCores = 1024

// ParseCPUCores parses a --cpus value (e.g. "0.5", "2", "2.5") into a
// validated core count.
func ParseCPUCores(s string) (float64, error) {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: cpus %q: %v", ErrInvalidResource, s, err)
	}
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, fmt.Errorf("%w: cpus %q: must be a finite number", ErrInvalidResource, s)
	}
	if v <= 0 {
		return 0, fmt.Errorf("%w: cpus %q: must be > 0", ErrInvalidResource, s)
	}
	if v > maxCPUCores {
		return 0, fmt.Errorf("%w: cpus %q: exceeds sanity ceiling of %d cores", ErrInvalidResource, s, maxCPUCores)
	}
	return v, nil
}

// maxMemoryBytes is a sanity ceiling (16 EiB is close to int64's own
// range; this catches unit-confusion overflow like a stray extra digit,
// not real hosts) — see ParseCPUCores's doc comment for the same rationale.
const maxMemoryBytes = int64(1) << 55

// ParseMemoryBytes parses a --memory value into a validated byte count.
// Accepted forms: a bare integer (bytes), or an integer immediately
// followed by one of the binary (IEC) unit suffixes Ki, Mi, Gi (1024-based
// — e.g. "256Mi" is 256*1024*1024 bytes). Deliberately not decimal
// suffixes (K/M/G) or the ambiguous Docker-style "m"/"g": accepting both a
// binary and a decimal reading of the same letter is exactly the kind of
// ambiguous input Phase 4 §9 requires rejecting outright rather than
// guessing.
func ParseMemoryBytes(s string) (int64, error) {
	orig := s
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("%w: memory %q: empty", ErrInvalidResource, orig)
	}

	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "Ki"):
		mult = 1024
		s = strings.TrimSuffix(s, "Ki")
	case strings.HasSuffix(s, "Mi"):
		mult = 1024 * 1024
		s = strings.TrimSuffix(s, "Mi")
	case strings.HasSuffix(s, "Gi"):
		mult = 1024 * 1024 * 1024
		s = strings.TrimSuffix(s, "Gi")
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: memory %q: %v", ErrInvalidResource, orig, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%w: memory %q: must be > 0", ErrInvalidResource, orig)
	}
	if n > maxMemoryBytes/mult {
		return 0, fmt.Errorf("%w: memory %q: overflows", ErrInvalidResource, orig)
	}
	return n * mult, nil
}

// ParsePIDsMax parses a --pids value into a validated task-count limit.
// Zero is rejected rather than treated as "unlimited" (which Resources'
// own zero value already means at the API level, for an *unset* flag) —
// an explicit "--pids 0" would forbid even glider-init itself from
// existing in the cgroup, which is never a usable configuration, so it is
// caught here rather than silently producing a container that can never
// start.
func ParsePIDsMax(s string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: pids %q: %v", ErrInvalidResource, s, err)
	}
	if n <= 0 {
		return 0, fmt.Errorf("%w: pids %q: must be > 0", ErrInvalidResource, s)
	}
	return n, nil
}

// cpuMaxValue formats cpu.max's contents (cgroup-v2-cpu.rst): "<quota>
// <period>" in microseconds, or "max <period>" for unlimited.
func cpuMaxValue(cores float64) string {
	if cores <= 0 {
		return fmt.Sprintf("max %d", cpuPeriodUsec)
	}
	quota := int64(cores * float64(cpuPeriodUsec))
	if quota < 1000 {
		// cgroup v2 requires a positive quota; guard against a very small
		// fractional core rounding down to (or below) zero.
		quota = 1000
	}
	return fmt.Sprintf("%d %d", quota, cpuPeriodUsec)
}

// memoryMaxValue formats memory.max's contents: "max" or a byte count.
func memoryMaxValue(bytes int64) string {
	if bytes <= 0 {
		return "max"
	}
	return strconv.FormatInt(bytes, 10)
}

// pidsMaxValue formats pids.max's contents: "max" or a task count.
func pidsMaxValue(n int64) string {
	if n <= 0 {
		return "max"
	}
	return strconv.FormatInt(n, 10)
}
