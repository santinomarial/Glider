//go:build linux

package cgroup

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadKeyValueFileTolerantOfUnknownFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cpu.stat")
	content := "usage_usec 12345\nuser_usec 6000\nsystem_usec 6345\nsome_future_field 999\nnr_periods 10\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readKeyValueFile(path)
	if err != nil {
		t.Fatalf("readKeyValueFile: %v", err)
	}
	if got["usage_usec"] != 12345 {
		t.Errorf("usage_usec = %d, want 12345", got["usage_usec"])
	}
	if got["nr_periods"] != 10 {
		t.Errorf("nr_periods = %d, want 10", got["nr_periods"])
	}
	// Unknown field must not cause an error and is simply present.
	if got["some_future_field"] != 999 {
		t.Errorf("some_future_field = %d, want 999", got["some_future_field"])
	}
}

func TestReadKeyValueFileDoesNotDependOnOrder(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pids.events")
	if err := os.WriteFile(path, []byte("max 7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readKeyValueFile(path)
	if err != nil {
		t.Fatalf("readKeyValueFile: %v", err)
	}
	if got["max"] != 7 {
		t.Errorf("max = %d, want 7", got["max"])
	}
}

func TestReadKeyValueFileMalformedKnownValue(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.events")
	if err := os.WriteFile(path, []byte("oom not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := readKeyValueFile(path)
	if err == nil {
		t.Fatal("expected error for malformed numeric value, got nil")
	}
}

func TestReadUintFileMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pids.current")
	if err := os.WriteFile(path, []byte("max\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readUintFile(path)
	if err != nil {
		t.Fatalf("readUintFile: %v", err)
	}
	if got != 0 {
		t.Errorf("readUintFile(\"max\") = %d, want 0", got)
	}
}

func TestReadUintFileNumeric(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "memory.current")
	if err := os.WriteFile(path, []byte("1048576\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readUintFile(path)
	if err != nil {
		t.Fatalf("readUintFile: %v", err)
	}
	if got != 1048576 {
		t.Errorf("readUintFile = %d, want 1048576", got)
	}
}

// TestManagerStatsAgainstFakeCgroupDir exercises Manager.Stats end-to-end
// against synthetic control files, without requiring a real cgroup —
// unit-level coverage of the parsing/assembly logic; real kernel-backed
// coverage is in the privileged integration suite.
func TestManagerStatsAgainstFakeCgroupDir(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef"
	containerDir := filepath.Join(root, id)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		"cpu.stat":       "usage_usec 100\nuser_usec 60\nsystem_usec 40\nnr_periods 5\nnr_throttled 2\nthrottled_usec 33\n",
		"memory.current": "2048\n",
		"memory.peak":    "4096\n",
		"memory.events":  "low 0\nhigh 0\nmax 1\noom 0\noom_kill 0\n",
		"pids.current":   "3\n",
		"pids.events":    "max 9\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(containerDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Manager{root: root}
	stats, err := m.Stats(id)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}

	if stats.CPU.UsageUsec != 100 || stats.CPU.NrThrottled != 2 || stats.CPU.ThrottledUsec != 33 {
		t.Errorf("CPU stats = %+v, unexpected", stats.CPU)
	}
	if stats.Memory.CurrentBytes != 2048 || stats.Memory.PeakBytes != 4096 {
		t.Errorf("Memory stats = %+v, unexpected", stats.Memory)
	}
	if stats.Memory.Events.Max != 1 {
		t.Errorf("Memory.Events.Max = %d, want 1", stats.Memory.Events.Max)
	}
	if stats.PIDs.Current != 3 || stats.PIDs.Events.Max != 9 {
		t.Errorf("PIDs stats = %+v, unexpected", stats.PIDs)
	}
}

// TestManagerStatsMissingOptionalPeak confirms memory.peak's absence
// (older kernel/config) is tolerated, not an error (Phase 4 §25).
func TestManagerStatsMissingOptionalPeak(t *testing.T) {
	root := t.TempDir()
	id := "0123456789abcdef"
	containerDir := filepath.Join(root, id)
	if err := os.MkdirAll(containerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"cpu.stat":       "usage_usec 0\n",
		"memory.current": "0\n",
		"memory.events":  "low 0\nhigh 0\nmax 0\noom 0\noom_kill 0\n",
		"pids.current":   "1\n",
		"pids.events":    "max 0\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(containerDir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	m := &Manager{root: root}
	stats, err := m.Stats(id)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Memory.PeakBytes != 0 {
		t.Errorf("PeakBytes = %d, want 0 (memory.peak absent)", stats.Memory.PeakBytes)
	}
}
