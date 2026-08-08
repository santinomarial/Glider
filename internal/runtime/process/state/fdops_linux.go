//go:build linux

package state

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"syscall"
)

// The *At functions below are fd-relative equivalents of Dir/Save/Load/
// TryLock, operating against an already-open directory file descriptor
// instead of a path.
//
// They exist for exactly one caller: glider-init (internal/runtime/process,
// docs/adr/0006), which durably records the container's EXITED transition
// itself — but only *after* pivot_root has already replaced its process's
// entire path-resolution root with the container's own rootfs. At that
// point the host-side state directory is no longer reachable by path at
// all (that's the whole point of pivot_root — see runtime.md §4). A file
// descriptor opened on that directory *before* pivot_root remains valid
// and fully usable via the openat(2) family regardless of pivot_root,
// since path resolution for `*at` syscalls with a directory fd operates
// relative to that fd, not the caller's current root. The caller is
// responsible for opening that fd before pivot_root and keeping it open
// (identity.go's callers in internal/runtime/process do this).
func SaveAt(dirFD int, rec Record) error {
	rec.SchemaVersion = SchemaVersion

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal state record: %w", err)
	}

	tmpName := fileName + ".tmp"
	fd, err := syscall.Openat(dirFD, tmpName, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("open temp state file: %w", err)
	}
	f := os.NewFile(uintptr(fd), tmpName)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write temp state file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync temp state file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp state file: %w", err)
	}
	if err := syscall.Renameat(dirFD, tmpName, dirFD, fileName); err != nil {
		return fmt.Errorf("publish state file: %w", err)
	}
	// Best-effort: fsync the directory itself so the rename survives a
	// crash immediately after this call returns, matching Save's guarantee.
	_ = syscall.Fsync(dirFD)

	return nil
}

// LoadAt is LoadAt's fd-relative counterpart to Load — see SaveAt's doc
// comment for why this exists.
func LoadAt(dirFD int) (Record, error) {
	var rec Record
	fd, err := syscall.Openat(dirFD, fileName, os.O_RDONLY, 0)
	if err != nil {
		return rec, err
	}
	f := os.NewFile(uintptr(fd), fileName)
	defer f.Close()

	data, err := io.ReadAll(f)
	if err != nil {
		return rec, err
	}
	if err := json.Unmarshal(data, &rec); err != nil {
		return rec, fmt.Errorf("%w: parse state file: %v", ErrCorruptState, err)
	}
	if rec.SchemaVersion != SchemaVersion {
		return rec, fmt.Errorf("%w: state file schema_version=%d, this binary supports %d",
			ErrUnsupportedVersion, rec.SchemaVersion, SchemaVersion)
	}
	if rec.ContainerID == "" {
		return rec, fmt.Errorf("%w: missing required field container_id", ErrCorruptState)
	}
	if !isKnownPhase(rec.Phase) {
		return rec, fmt.Errorf("%w: unknown phase %q", ErrCorruptState, rec.Phase)
	}
	return rec, nil
}

// LockAt is TryLock's fd-relative counterpart — see SaveAt's doc comment.
func LockAt(dirFD int) (*Lock, error) {
	fd, err := syscall.Openat(dirFD, lockFileName, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(fd, syscall.LOCK_EX); err != nil {
		syscall.Close(fd)
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &Lock{f: os.NewFile(uintptr(fd), lockFileName)}, nil
}
