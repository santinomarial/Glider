//go:build linux

package cgroup

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// findMountPoint locates the cgroup v2 unified hierarchy's mount point by
// parsing /proc/self/mountinfo, rather than assuming /sys/fs/cgroup
// (Phase 4 §4): a host could mount it elsewhere, and unconditionally
// hard-coding the conventional path would silently do the wrong thing on
// one that doesn't.
//
// mountinfo's format (proc_pid_mountinfo(5)) is a fixed set of
// space-separated fields followed by a literal "-" separator and then the
// filesystem type; cgroup2 is one of the few virtual filesystems with a
// single, non-per-controller mount point, so the first "cgroup2"-typed
// entry found is authoritative — cgroup v1 hosts have per-controller
// "cgroup" (not "cgroup2") mounts instead, which this deliberately does
// not match (ADR-0001: cgroup v2 only, never a v1 fallback).
func findMountPoint() (string, error) {
	f, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return "", fmt.Errorf("read /proc/self/mountinfo: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		sepIdx := strings.Index(line, " - ")
		if sepIdx < 0 {
			continue
		}
		fields := strings.Fields(line[:sepIdx])
		rest := strings.Fields(line[sepIdx+3:])
		if len(fields) < 5 || len(rest) < 1 {
			continue
		}
		if rest[0] != "cgroup2" {
			continue
		}
		return fields[4], nil // field 5 (index 4): mount point
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan /proc/self/mountinfo: %w", err)
	}
	return "", fmt.Errorf("%w: no cgroup2 mount found in /proc/self/mountinfo", ErrUnsupported)
}

// hasUnifiedControllers is a cheap, fast-failing sanity check that the
// discovered mount actually exposes the unified hierarchy's controllers
// file — matching runtime.md's existing pre-Phase-4 check, kept as a
// belt-and-braces validation now that discovery no longer assumes a fixed
// path.
func hasUnifiedControllers(mountPoint string) error {
	if _, err := os.Stat(mountPoint + "/cgroup.controllers"); err != nil {
		return fmt.Errorf("%w: %s/cgroup.controllers not found: %v", ErrUnsupported, mountPoint, err)
	}
	return nil
}
