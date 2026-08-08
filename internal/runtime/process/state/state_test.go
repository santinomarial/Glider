package state

import (
	"errors"
	"fmt"
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
		{Exited, Deleting, true},
		{Failed, Deleting, true},
		{Exited, Creating, false},
		{Deleting, Creating, false}, // terminal; convergence to ABSENT is file removal, not a transition
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
		ContainerID:   "deadbeefcafef00d",
		Phase:         Running,
		RootFS:        "/var/lib/glider/rootfs/test",
		Argv:          []string{"/bin/sh", "-c", "echo hi"},
		CreatedAt:     time.Now().UTC().Truncate(time.Second),
		UpdatedAt:     time.Now().UTC().Truncate(time.Second),
		InitPID:       12345,
		InitStartTime: 999,
		ExitCode:      &code,
	}

	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got.ContainerID != rec.ContainerID || got.Phase != rec.Phase || got.InitPID != rec.InitPID {
		t.Fatalf("round-tripped record mismatch: got %+v, want %+v", got, rec)
	}
	if got.ExitCode == nil || *got.ExitCode != *rec.ExitCode {
		t.Fatalf("exit code not round-tripped: got %+v", got.ExitCode)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d (Save must stamp current version)", got.SchemaVersion, SchemaVersion)
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

// --- state corruption safety (Phase 2 §29/§43.N): malformed state must
// fail safely and distinguishably, never be guessed at. ---

func writeRaw(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadTruncatedJSON(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, `{"container_id": "abc", "phase": "RUNNING", "schema_ver`)

	_, err := Load(dir)
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Load truncated file: got %v, want ErrCorruptState", err)
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, `not json at all`)

	_, err := Load(dir)
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Load invalid JSON: got %v, want ErrCorruptState", err)
	}
}

func TestLoadUnknownSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, `{"schema_version": 999, "container_id": "abc", "phase": "RUNNING"}`)

	_, err := Load(dir)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Load future-version file: got %v, want ErrUnsupportedVersion", err)
	}
}

func TestLoadMissingSchemaVersionIsPhase1Format(t *testing.T) {
	// Phase 1 wrote no schema_version field at all; it must not be
	// silently accepted as if it were the current (differently-meaning)
	// format.
	dir := t.TempDir()
	writeRaw(t, dir, `{"container_id": "abc", "phase": "RUNNING", "pid": 123}`)

	_, err := Load(dir)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("Load Phase-1-format file: got %v, want ErrUnsupportedVersion", err)
	}
}

func TestLoadMissingRequiredField(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, fmt.Sprintf(`{"schema_version": %d, "phase": "RUNNING"}`, SchemaVersion))

	_, err := Load(dir)
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Load record missing container_id: got %v, want ErrCorruptState", err)
	}
}

func TestLoadInvalidPhase(t *testing.T) {
	dir := t.TempDir()
	writeRaw(t, dir, fmt.Sprintf(`{"schema_version": %d, "container_id": "abc", "phase": "SIDEWAYS"}`, SchemaVersion))

	_, err := Load(dir)
	if !errors.Is(err, ErrCorruptState) {
		t.Fatalf("Load record with invalid phase: got %v, want ErrCorruptState", err)
	}
}

// --- per-container locking (§26/§27) ---

func TestLockExclusion(t *testing.T) {
	dir := t.TempDir()

	l1, err := TryLock(dir)
	if err != nil {
		t.Fatalf("first TryLock: %v", err)
	}
	defer l1.Unlock()

	_, err = TryLock(dir)
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("second concurrent TryLock: got %v, want ErrBusy", err)
	}
}

func TestLockReleaseAllowsReacquire(t *testing.T) {
	dir := t.TempDir()

	l1, err := TryLock(dir)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if err := l1.Unlock(); err != nil {
		t.Fatalf("Unlock: %v", err)
	}

	l2, err := TryLock(dir)
	if err != nil {
		t.Fatalf("TryLock after release: %v", err)
	}
	defer l2.Unlock()
}
