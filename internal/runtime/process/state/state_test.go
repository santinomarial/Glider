package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestValidTransitionFollowsContainerLifecycle(t *testing.T) {
	cases := []struct {
		from, to Phase
		want     bool
	}{
		{"", Creating, true}, // ABSENT -> CREATING
		{"", Created, false}, // ABSENT can only go to CREATING
		{Creating, Created, true},
		{Creating, Failed, true},
		{Creating, Running, false}, // must pass through CREATED
		{Created, Running, true},
		{Created, Failed, true},
		{Created, Exited, false}, // must pass through RUNNING
		{Running, Stopping, true},
		{Running, Exited, true},
		{Running, Failed, true},
		{Running, Creating, false}, // no going backwards
		{Stopping, Exited, true},
		{Stopping, Failed, true},
		{Stopping, Running, false},
		{Exited, Creating, false}, // terminal for Phase 1's foreground run
	}

	for _, c := range cases {
		got := ValidTransition(c.from, c.to)
		if got != c.want {
			t.Errorf("ValidTransition(%q, %q) = %v, want %v", c.from, c.to, got, c.want)
		}
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	code := 0
	rec := Record{
		ContainerID: "deadbeefcafef00d",
		Phase:       Running,
		RootFS:      "/var/lib/glider/rootfs/test",
		Argv:        []string{"/bin/sh", "-c", "echo hi"},
		CreatedAt:   time.Now().UTC().Truncate(time.Second),
		UpdatedAt:   time.Now().UTC().Truncate(time.Second),
		Pid:         12345,
		ExitCode:    &code,
	}

	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.ContainerID != rec.ContainerID || got.Phase != rec.Phase || got.Pid != rec.Pid {
		t.Fatalf("round-tripped record mismatch: got %+v, want %+v", got, rec)
	}
	if got.ExitCode == nil || *got.ExitCode != *rec.ExitCode {
		t.Fatalf("exit code not round-tripped: got %+v", got.ExitCode)
	}
}

func TestSaveIsAtomicNoTempFileLeftBehind(t *testing.T) {
	dir := t.TempDir()
	rec := Record{ContainerID: "abc123", Phase: Creating}
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Fatalf("expected exactly state.json in %s, got %v", dir, entries)
	}
}

func TestLoadMissingIsNotExist(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(filepath.Join(dir, "does-not-exist"))
	if err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected IsNotExist error, got %v", err)
	}
}
