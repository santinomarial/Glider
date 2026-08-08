//go:build linux

package process

import "syscall"

// reapExited performs one non-blocking sweep of exited children
// (wait4(-1, WNOHANG) in a loop until none remain), the event-driven
// zombie-reaping mechanism runtime.md §5 requires: glider-init is PID 1 of
// its namespace, so every process that gets reparented to it (an orphaned
// grandchild whose own parent exited) must eventually be reaped here too,
// not just the main workload.
//
// If mainPID's exit is observed during this sweep, its wait status is
// returned via (status, true); the sweep still continues draining any
// other already-exited children before returning, so a burst of
// simultaneous exits (main workload plus stragglers) doesn't leave zombies
// behind just because the main one was seen first.
func reapExited(mainPID int) (mainStatus syscall.WaitStatus, mainExited bool) {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err != nil {
			// ECHILD: no children left at all. Any other error is not
			// expected here (we only ever wait on our own children) and is
			// not actionable beyond stopping the sweep.
			return mainStatus, mainExited
		}
		if pid <= 0 {
			// 0: WNOHANG and nothing currently exited. Nothing left to
			// drain this sweep.
			return mainStatus, mainExited
		}
		if pid == mainPID {
			mainStatus = ws
			mainExited = true
		}
	}
}

// exitCodeFromWaitStatus maps a workload's raw wait(2) status to the exit
// code Glider reports, per runtime.md/container-lifecycle.md: a normal
// exit reports its own status; termination by an uncaught signal reports
// 128+signal (the standard shell convention), so operators can distinguish
// "exited 137" (looks like OOM-kill, SIGKILL) from an ambiguous flat
// nonzero code.
func exitCodeFromWaitStatus(ws syscall.WaitStatus) int {
	switch {
	case ws.Exited():
		return ws.ExitStatus()
	case ws.Signaled():
		return 128 + int(ws.Signal())
	default:
		return -1
	}
}

// finalDrain performs a bounded, non-blocking sweep to reap any
// already-exited stragglers after the main workload has terminated, before
// glider-init itself exits. It is intentionally bounded (not a blocking
// wait for every descendant): container-lifecycle.md's Phase 2 policy
// (docs/adr/0006) is that glider-init does not wait indefinitely for
// surviving descendants once the main workload is done — any process still
// alive in the namespace is forcibly terminated by the kernel the moment
// glider-init (namespace PID 1) itself exits, so there is nothing left to
// leak regardless of what this sweep catches.
func finalDrain() {
	const maxSweep = 4096 // generous bound against a pathological fork bomb
	for i := 0; i < maxSweep; i++ {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if err != nil || pid <= 0 {
			return
		}
	}
}
