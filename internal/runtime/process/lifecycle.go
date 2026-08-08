//go:build linux

package process

import (
	"fmt"
	"time"

	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// lockTimeout bounds how long a lifecycle write waits on the per-container
// lock before giving up (state/lock.go's LockWithTimeout doc comment
// explains why this is contention backoff, not a sequencing barrier).
// Hold times on this lock are always one JSON write + fsync + rename, so a
// generous-looking bound here is still fast in the overwhelmingly common
// uncontended case and only matters when something is actually stuck.
const lockTimeout = 3 * time.Second

// applyTransition validates and mutates rec's Phase/UpdatedAt in memory,
// without acquiring the container lock or persisting — for callers that
// already hold the lock for a larger critical section (recovery.go) and
// must not attempt to re-acquire it (flock is scoped per open file
// description, not per-process: a second acquisition attempt from the same
// process would contend with, not reuse, the first).
func applyTransition(rec *state.Record, to state.Phase) error {
	if !state.ValidTransition(rec.Phase, to) {
		return fmt.Errorf("invalid container lifecycle transition %q -> %q", rec.Phase, to)
	}
	rec.Phase = to
	rec.UpdatedAt = time.Now()
	return nil
}

// saveTransition validates and persists a lifecycle move per
// container-lifecycle.md's transition table, under the container's
// advisory lock, so an implementation bug that tries an illegal transition
// fails loudly instead of silently corrupting the recorded lifecycle, and
// two independent writers (the launcher and glider-init itself — see
// docs/adr/0006) never race a write. Used by callers that do NOT already
// hold the lock (launcher.go, glider-init's persistExited) — see
// applyTransition for the alternative used by callers that do.
func saveTransition(dir string, rec *state.Record, to state.Phase) error {
	lock, err := state.LockWithTimeout(dir, lockTimeout)
	if err != nil {
		return fmt.Errorf("acquire container state lock for %s transition: %w", to, err)
	}
	defer lock.Unlock()

	if err := applyTransition(rec, to); err != nil {
		return err
	}
	if err := state.Save(dir, *rec); err != nil {
		return fmt.Errorf("persist %s state: %w", to, err)
	}
	return nil
}
