package logfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriterRotatesAndBoundsFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.log")
	w, err := New(path, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []string{"1234", "5678", "90"} {
		if _, err := w.Write([]byte(v)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2"} {
		if _, err := os.Stat(name); err != nil {
			t.Fatalf("missing %s: %v", name, err)
		}
	}
	if _, err := os.Stat(path + ".3"); !os.IsNotExist(err) {
		t.Fatal("too many backups")
	}
}
