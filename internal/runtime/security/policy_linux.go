//go:build linux

// Package security installs Glider's default workload security boundary.
// It is applied after namespace/rootfs setup, in the workload child, directly
// before execve. glider-init retains only the privileges it needs to supervise.
package security

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

var ErrUnsupportedArchitecture = errors.New("unsupported seccomp architecture")

// DefaultCapabilities is intentionally smaller than Docker's historical
// default. It supports ordinary root-owned image setup and low-port binding,
// but excludes host/network administration, mount, ptrace, and raw sockets.
var DefaultCapabilities = []int{
	unix.CAP_CHOWN,
	unix.CAP_DAC_OVERRIDE,
	unix.CAP_FOWNER,
	unix.CAP_FSETID,
	unix.CAP_KILL,
	unix.CAP_SETGID,
	unix.CAP_SETUID,
	unix.CAP_NET_BIND_SERVICE,
}

func ApplyDefault() error {
	if err := reduceCapabilities(DefaultCapabilities); err != nil { return fmt.Errorf("reduce capabilities: %w", err) }
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil { return fmt.Errorf("set no_new_privs: %w", err) }
	if err := installDefaultSeccomp(); err != nil { return fmt.Errorf("install seccomp: %w", err) }
	return nil
}

func reduceCapabilities(allowed []int) error {
	last := 63
	if raw, err := os.ReadFile("/proc/sys/kernel/cap_last_cap"); err == nil { if n, parseErr := strconv.Atoi(strings.TrimSpace(string(raw))); parseErr == nil { last = n } }
	keep := make(map[int]bool, len(allowed))
	for _, cap := range allowed { if cap < 0 || cap > last { return fmt.Errorf("invalid capability %d", cap) }; keep[cap] = true }
	for cap := 0; cap <= last; cap++ { if !keep[cap] { if err := unix.Prctl(unix.PR_CAPBSET_DROP, uintptr(cap), 0, 0, 0); err != nil && !errors.Is(err, syscall.EINVAL) { return fmt.Errorf("drop capability %d from bounding set: %w", cap, err) } } }
	var data [2]unix.CapUserData
	for cap := range keep { idx, bit := cap/32, uint(cap%32); data[idx].Effective |= 1 << bit; data[idx].Permitted |= 1 << bit }
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3, Pid: 0}
	if err := unix.Capset(&header, &data[0]); err != nil { return err }
	const prCapAmbient = 47
	const prCapAmbientClearAll = 4
	if err := unix.Prctl(prCapAmbient, prCapAmbientClearAll, 0, 0, 0); err != nil && !errors.Is(err, syscall.EINVAL) { return fmt.Errorf("clear ambient capabilities: %w", err) }
	return nil
}

func installDefaultSeccomp() error {
	arch, blocked, err := architecturePolicy()
	if err != nil { return err }
	filter := []unix.SockFilter{
		{Code: bpfLDWABS, K: 4},
		{Code: bpfJMPJEQK, Jt: 1, K: arch},
		{Code: bpfRETK, K: seccompRetKillProcess},
		{Code: bpfLDWABS, K: 0},
	}
	for _, nr := range blocked { filter = append(filter, unix.SockFilter{Code:bpfJMPJEQK, Jf:1, K:uint32(nr)}, unix.SockFilter{Code:bpfRETK, K:seccompRetErrno|uint32(syscall.EPERM)}) }
	filter = append(filter, unix.SockFilter{Code:bpfRETK, K:seccompRetAllow})
	program := unix.SockFprog{Len:uint16(len(filter)), Filter:&filter[0]}
	if err := unix.Prctl(unix.PR_SET_SECCOMP, unix.SECCOMP_MODE_FILTER, uintptr(unsafe.Pointer(&program)), 0, 0); err != nil { return err }
	return nil
}

const (
	bpfLDWABS = 0x20
	bpfJMPJEQK = 0x15
	bpfRETK = 0x06
	seccompRetKillProcess = 0x80000000
	seccompRetErrno = 0x00050000
	seccompRetAllow = 0x7fff0000
)
