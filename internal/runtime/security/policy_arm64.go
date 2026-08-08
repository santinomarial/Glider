//go:build linux && arm64

package security

import "golang.org/x/sys/unix"

func architecturePolicy() (uint32, []uintptr, error) {
	const auditArchAArch64 = 0xc00000b7
	return auditArchAArch64, []uintptr{
		unix.SYS_PTRACE, unix.SYS_MOUNT, unix.SYS_UMOUNT2,
		unix.SYS_PIVOT_ROOT, unix.SYS_REBOOT, unix.SYS_INIT_MODULE,
		unix.SYS_FINIT_MODULE, unix.SYS_DELETE_MODULE, unix.SYS_KEXEC_LOAD,
		unix.SYS_OPEN_BY_HANDLE_AT, unix.SYS_UNSHARE, unix.SYS_SETNS,
		unix.SYS_BPF, unix.SYS_PERF_EVENT_OPEN,
	}, nil
}
