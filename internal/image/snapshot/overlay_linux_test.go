//go:build linux

package snapshot

import (
	"errors"
	"os"
	"path/filepath"
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
