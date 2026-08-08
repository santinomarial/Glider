//go:build linux

package process

import (
	"errors"
	"fmt"
	"os"

	"github.com/santinomarial/glider/internal/runtime/process/state"
)

// ErrContainerNotFound means Recover was asked about a container with no
// durable state record — ABSENT is not a recoverable condition, it's the
// natural terminal state after a successful DELETING convergence (or a
// container that never existed).
var ErrContainerNotFound = errors.New("container not found")

// RecoveryAction summarizes what Recover concluded and did.
type RecoveryAction string

const (
	// RecoveryStillHealthy means the recorded owning process (glider-init)
	// is still alive and its identity matches — nothing to do. Applies to
	// CREATING/CREATED (rare — recovery normally only runs after a crash,
	// and a genuinely mid-flight CREATING/CREATED with a live process
	// most likely means recovery raced a legitimate in-progress launch;
	// see recoverPreRunning) and to RUNNING/STOPPING.
	RecoveryStillHealthy RecoveryAction = "STILL_HEALTHY"
	// RecoveryConvergedFailed means a CREATING/CREATED container whose
	// owning process is confirmed gone was converged to FAILED — setup
	// never reached a running workload (container-lifecycle.md §4).
	RecoveryConvergedFailed RecoveryAction = "CONVERGED_FAILED"
	// RecoveryConvergedExited means a RUNNING/STOPPING container whose
	// glider-init is confirmed gone was converged to EXITED with an
	// inferred (unknown) exit code — see docs/adr/0006's kernel-liveness
	// guarantee for why this is safe without inspecting anything else.
	RecoveryConvergedExited RecoveryAction = "CONVERGED_EXITED"
	// RecoveryCleanedUp means an EXITED/FAILED/DELETING container was
	// (re)converged through DELETING to ABSENT — the state record is gone.
	RecoveryCleanedUp RecoveryAction = "CLEANED_UP"
)

// RecoveryResult reports what Recover did for one container.
type RecoveryResult struct {
	ContainerID string
	Action      RecoveryAction
	// Record is the container's final state record. Zero value when Action
	// is RecoveryCleanedUp (the record no longer exists).
	Record state.Record
}

// Recover inspects one container's durable state and, per
// container-lifecycle.md §4 (extended for Phase 2 by docs/adr/0006's
// kernel-liveness-equivalence property), converges it to a safe state if
// its owning process is gone. It is the smallest Phase 2 recovery surface
// — there is no gliderd/reconciler yet (master plan Phase 10) to run this
// automatically; it is invoked explicitly, by an operator
// (cmd/glider-runtime's `recover` subcommand) or a test, after a launcher
// crash.
//
// Recover holds the container's advisory lock (state/lock.go) for its
// entire operation, so two concurrent Recover calls against the same
// container — or a Recover racing a live launcher's own transitions —
// serialize rather than race destructively (Phase 2 §26/§27).
func Recover(stateRoot, containerID string) (RecoveryResult, error) {
	dir := state.Dir(stateRoot, containerID)

	lock, err := state.LockWithTimeout(dir, lockTimeout)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// TryLock deliberately never creates the container directory
			// (state/lock.go) — a missing directory means this container
			// was never created, or has already been fully deleted by a
			// prior Recover call (idempotent convergence, Phase 2 §27).
			return RecoveryResult{}, fmt.Errorf("%w: %s", ErrContainerNotFound, containerID)
		}
		return RecoveryResult{}, fmt.Errorf("acquire container lock for recovery: %w", err)
	}
	defer lock.Unlock()

	rec, err := state.Load(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return RecoveryResult{}, fmt.Errorf("%w: %s", ErrContainerNotFound, containerID)
		}
		return RecoveryResult{}, fmt.Errorf("load container state: %w", err)
	}

	switch rec.Phase {
	case state.Creating, state.Created:
		return recoverPreRunning(dir, rec)
	case state.Running, state.Stopping:
		return recoverRunning(dir, rec)
	case state.Exited, state.Failed:
		if err := applyTransition(&rec, state.Deleting); err != nil {
			return RecoveryResult{}, err
		}
		if err := state.Save(dir, rec); err != nil {
			return RecoveryResult{}, fmt.Errorf("persist DELETING state: %w", err)
		}
		return finishDeleting(dir, rec)
	case state.Deleting:
		return finishDeleting(dir, rec)
	default:
		return RecoveryResult{}, fmt.Errorf("unrecognized phase %q", rec.Phase)
	}
}

// recoverPreRunning handles CREATING and CREATED (container-lifecycle.md
// §4's "CREATING, no further evidence" case, and Phase 2 §15/§16's
// deferred-from-Phase-1 CREATED case).
//
// CREATING never has a recorded InitPID (it's only captured once CREATED
// is reached — launcher.go), so its identity check below always resolves
// to "not alive", converging straight to FAILED — matching
// container-lifecycle.md §4 exactly ("no further evidence: treat as
// FAILED").
func recoverPreRunning(dir string, rec state.Record) (RecoveryResult, error) {
	id := ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime}
	alive := false
	if !id.IsZero() {
		var err error
		alive, err = ValidateProcessIdentity(id)
		if err != nil {
			return RecoveryResult{}, fmt.Errorf("validate init identity: %w", err)
		}
	}
	if alive {
		// glider-init exists and might still be legitimately mid-flight
		// under its original, still-alive launcher (recovery is not
		// exclusive with a concurrent legitimate launch — the lock above
		// only prevents two recovery/mutation attempts from racing each
		// other, not from observing an in-progress one). Nothing safe to
		// do but report the observation; Phase 2 §15 explicitly rejects
		// "magical resume" here.
		return RecoveryResult{ContainerID: rec.ContainerID, Action: RecoveryStillHealthy, Record: rec}, nil
	}

	rec.Error = "recovered: launcher/init lost before workload start"
	if err := applyTransition(&rec, state.Failed); err != nil {
		return RecoveryResult{}, err
	}
	if err := state.Save(dir, rec); err != nil {
		return RecoveryResult{}, fmt.Errorf("persist recovered state: %w", err)
	}
	return RecoveryResult{ContainerID: rec.ContainerID, Action: RecoveryConvergedFailed, Record: rec}, nil
}

// recoverRunning handles RUNNING and STOPPING per docs/adr/0006: a live,
// identity-matching glider-init needs no adoption (it already supervises
// independently of any launcher); a gone one means the entire container is
// gone too (kernel PID-namespace-init-exit guarantee), converged to EXITED
// with an inferred exit code rather than a guessed one.
func recoverRunning(dir string, rec state.Record) (RecoveryResult, error) {
	id := ProcessIdentity{PID: rec.InitPID, StartTime: rec.InitStartTime}
	alive, err := ValidateProcessIdentity(id)
	if err != nil {
		return RecoveryResult{}, fmt.Errorf("validate init identity: %w", err)
	}
	if alive {
		return RecoveryResult{ContainerID: rec.ContainerID, Action: RecoveryStillHealthy, Record: rec}, nil
	}

	rec.ExitCode = nil
	rec.ExitedInferred = true
	if err := applyTransition(&rec, state.Exited); err != nil {
		return RecoveryResult{}, err
	}
	if err := state.Save(dir, rec); err != nil {
		return RecoveryResult{}, fmt.Errorf("persist recovered state: %w", err)
	}
	return RecoveryResult{ContainerID: rec.ContainerID, Action: RecoveryConvergedExited, Record: rec}, nil
}

// finishDeleting runs (or resumes) idempotent cleanup and, once confirmed
// complete, removes the state record entirely (convergence to ABSENT —
// container-lifecycle.md §3: "the record of absence is durable only after
// the resource is gone").
func finishDeleting(dir string, rec state.Record) (RecoveryResult, error) {
	if err := cleanupContainer(rec); err != nil {
		return RecoveryResult{}, fmt.Errorf("cleanup: %w", err)
	}
	if err := os.RemoveAll(dir); err != nil {
		return RecoveryResult{}, fmt.Errorf("remove container state dir: %w", err)
	}
	return RecoveryResult{ContainerID: rec.ContainerID, Action: RecoveryCleanedUp}, nil
}
