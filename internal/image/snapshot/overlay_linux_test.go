//go:build linux

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPathsRejectUnsafeID(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "a b"} {
		if _, err := m.paths(id); !errors.Is(err, ErrInvalidSnapshot) {
			t.Errorf("paths(%q) = %v", id, err)
		}
	}
}

func TestOverlaySnapshotsShareLowersButIsolateWrites(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("OverlayFS mount requires root")
	}
	base := filepath.Join(t.TempDir(), "base")
	top := filepath.Join(t.TempDir(), "top")
	for _, dir := range []string{base, top} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(base, "common"), []byte("base"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(top, "common"), []byte("top"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, _ := NewManager(t.TempDir())
	one, err := m.Ensure("one", []string{base, top})
	if errors.Is(err, syscall.EPERM) && os.Getenv("GLIDER_REQUIRE_PRIVILEGED_TESTS") != "1" {
		t.Skip("mount not permitted")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer m.Remove("one")
	got, err := os.ReadFile(filepath.Join(one.Merged, "common"))
	if err != nil || string(got) != "top" {
		t.Fatalf("precedence=%q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(one.Merged, "private"), []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	two, err := m.Ensure("two", []string{base, top})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Remove("two")
	if _, err := os.Stat(filepath.Join(two.Merged, "private")); !os.IsNotExist(err) {
		t.Fatalf("write leaked to second snapshot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(base, "private")); !os.IsNotExist(err) {
		t.Fatalf("write leaked to lower: %v", err)
	}
}

func TestRecoverCleansPartialUnMountedSnapshot(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	s, _ := m.paths("container-1")
	for _, dir := range []string{s.Upper, s.Work, s.Merged} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := saveRecord(filepath.Dir(s.Upper), Record{Version: 1, ID: "container-1", LowerDirs: []string{"/layer"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Recover("container-1"); !os.IsNotExist(err) {
		t.Fatalf("Recover error=%v, want not exist", err)
	}
	if _, err := os.Stat(filepath.Dir(s.Upper)); !os.IsNotExist(err) {
		t.Fatalf("partial snapshot remains: %v", err)
	}
}

func TestEqualStringsOrderIsSignificant(t *testing.T) {
	if !equalStrings([]string{"base", "top"}, []string{"base", "top"}) {
		t.Fatal("equal slices differ")
	}
	if equalStrings([]string{"base", "top"}, []string{"top", "base"}) {
		t.Fatal("layer order must be significant")
	}
}

func TestActiveLowerDirsFailsClosedAndReturnsReferences(t *testing.T) {
	m, _ := NewManager(t.TempDir())
	dir := filepath.Join(m.root, "container-1")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	lowers := []string{"/layers/sha256/one", "/layers/sha256/two"}
	if err := saveRecord(dir, Record{Version: 1, ID: "container-1", LowerDirs: lowers}); err != nil {
		t.Fatal(err)
	}
	active, err := m.ActiveLowerDirs()
	if err != nil {
		t.Fatal(err)
	}
	for _, lower := range lowers {
		if _, ok := active[lower]; !ok {
			t.Fatalf("missing lower %s", lower)
		}
	}
	bad := filepath.Join(m.root, "container-2")
	if err := os.Mkdir(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, "state.json"), []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := m.ActiveLowerDirs(); err == nil {
		t.Fatal("corrupt snapshot record did not stop collection")
	}
}
