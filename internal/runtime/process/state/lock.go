package state

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// ErrBusy means another process already holds the container's lock.
var ErrBusy = errors.New("container is locked by another process")

// Lock is a per-container advisory lock (flock(2)) used to serialize
// concurrent lifecycle-mutating operations — a launcher and a recovery
// pass, or two concurrent recovery attempts — against the same container
// ID, so they cannot race destructively.
//
// flock is chosen over a PID-file/content-based scheme deliberately: its
// crash behavior is exactly what's needed here. The kernel releases the
// lock automatically when the holding process's last file descriptor on it
// closes, including on a crash or SIGKILL, so there is never a stale lock
// to detect or clean up by hand — the lock's validity is tied directly to
// the holding process's liveness by the kernel itself. This is node-local
// concurrency control, not distributed consensus.
type Lock struct {
	f *os.File
}

// TryLock attempts to acquire dir's container lock without blocking. If
// another process already holds it, it returns ErrBusy — a normal,
// expected outcome callers should surface as an explicit ownership
// conflict, never silently retry into a race.
//
// TryLock deliberately does NOT create dir if it's missing: a container
// directory is created exactly once, by the CREATING transition (the only
// writer allowed to bring a container into existence). If TryLock created
// it on demand, locking a container that was already fully deleted (Recover
// converging EXITED/FAILED through DELETING to ABSENT) would silently
// resurrect an empty directory — every subsequent operation on that ID
// would then see a directory as "evidence" of something that no longer
// exists. A missing dir is reported as a plain *PathError (os.IsNotExist
// applies) so callers (Recover) can map it to "container not found"
// distinctly from ErrBusy.
func TryLock(dir string) (*Lock, error) {
	f, err := os.OpenFile(LockPath(dir), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrBusy
		}
		return nil, fmt.Errorf("flock: %w", err)
	}
	return &Lock{f: f}, nil
}

// LockWithTimeout acquires dir's container lock, retrying on contention
// with a short bounded backoff until timeout elapses. This is NOT a
// sequencing/correctness barrier of the kind container-lifecycle.md
// disallows sleep-based synchronization for (runtime.md §3) — it is
// ordinary contention backoff on an advisory lock whose hold times are
// always brief (one JSON write + fsync + rename), used so two independent
// writers (a launcher and glider-init, or two recovery attempts) never
// silently race a state-file write; a caller that can't acquire it within
// timeout gets ErrBusy and must treat that as an explicit, surfaced
// condition, never retry silently forever.
func LockWithTimeout(dir string, timeout time.Duration) (*Lock, error) {
	deadline := time.Now().Add(timeout)
	backoff := time.Millisecond
	for {
		l, err := TryLock(dir)
		if err == nil {
			return l, nil
		}
		if !errors.Is(err, ErrBusy) {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, ErrBusy
		}
		time.Sleep(backoff)
		if backoff < 50*time.Millisecond {
			backoff *= 2
		}
	}
}

// Unlock releases the lock. Safe to call once on a non-nil Lock; the lock
// is also released automatically (by the kernel) if the process dies
// without calling it.
func (l *Lock) Unlock() error {
	if l == nil || l.f == nil {
		return nil
	}
	unlockErr := syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	closeErr := l.f.Close()
	if unlockErr != nil {
		return fmt.Errorf("unlock: %w", unlockErr)
	}
	return closeErr
}
