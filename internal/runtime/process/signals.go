//go:build linux

package process

import (
	"os"
	"syscall"
)

// Signal policy (runtime.md §5, docs/adr/0006). glider-init deliberately
// does not forward every signal it could receive — only a fixed, documented
// set, split into two categories:

// lifecycleSignals trigger the container's graceful-shutdown protocol
// (§6): the signal is forwarded once to the workload's process group, a
// grace-period timer starts, and failure to exit within it escalates to
// SIGKILL. These are the signals `container-lifecycle.md`'s STOPPING state
// is entered for.
var lifecycleSignals = []os.Signal{syscall.SIGTERM, syscall.SIGINT}

// transparentSignals are forwarded to the workload's process group exactly
// once, with no shutdown implication — the workload decides what (if
// anything) to do with them. SIGHUP (terminal hangup / conventional
// "reload config") and SIGQUIT (quit-with-core-dump) are common enough
// process-lifecycle signals that a supervised workload expects to receive
// them like any normal child of a shell; SIGWINCH (terminal resize) matters
// only to workloads attached to a TTY and is harmless to forward
// unconditionally otherwise.
//
// Deliberately NOT forwarded: SIGKILL/SIGSTOP (uncatchable, nothing to
// forward — the kernel already delivers these directly), SIGCHLD (consumed
// by glider-init's own reaper, forwarding it to the workload would be
// meaningless), and anything else not in this list — "forward every
// possible signal" was explicitly rejected as the policy (runtime.md §5,
// Phase 2 brief §4).
var transparentSignals = []os.Signal{syscall.SIGHUP, syscall.SIGQUIT, syscall.SIGWINCH}

// forwardedSignals is every signal glider-init installs a handler for,
// excluding SIGCHLD (handled separately by the reaper, not as a
// forward-to-workload signal).
func forwardedSignals() []os.Signal {
	sigs := make([]os.Signal, 0, len(lifecycleSignals)+len(transparentSignals))
	sigs = append(sigs, lifecycleSignals...)
	sigs = append(sigs, transparentSignals...)
	return sigs
}

func isLifecycleSignal(sig os.Signal) bool {
	for _, s := range lifecycleSignals {
		if s == sig {
			return true
		}
	}
	return false
}
