//go:build linux

// Package namespace defines the set of Linux namespaces glider-runtime
// places a container into, per docs/design/runtime.md §2.
package namespace

import "syscall"

// Phase1Flags returns the clone(2) namespace flags used to launch a Phase 1
// container: PID, mount, UTS, IPC, network, and cgroup namespaces
// (runtime.md §2). The network and cgroup namespaces are created but left
// unconfigured in Phase 1 — no veth/bridge attachment (Phase 9) and no
// cgroup resource enforcement (Phase 4) — per the same table. User
// namespaces are deliberately excluded (runtime.md §2, master plan §9).
func Phase1Flags() uintptr {
	return syscall.CLONE_NEWPID |
		syscall.CLONE_NEWNS |
		syscall.CLONE_NEWUTS |
		syscall.CLONE_NEWIPC |
		syscall.CLONE_NEWNET |
		syscall.CLONE_NEWCGROUP
}
