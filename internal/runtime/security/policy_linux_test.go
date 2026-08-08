//go:build linux && amd64

package security

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestArchitecturePolicyBlocksLoadBearingSyscalls(t *testing.T) {
	arch, blocked, err := architecturePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if arch != 0xc000003e {
		t.Fatalf("arch=%#x", arch)
	}
	if len(blocked) < 10 {
		t.Fatalf("blocked only %d syscalls", len(blocked))
	}
	seen := map[uintptr]bool{}
	for _, nr := range blocked {
		if seen[nr] {
			t.Fatalf("duplicate syscall %d", nr)
		}
		seen[nr] = true
	}
}

func TestDefaultPolicyEnforcesNoNewPrivsAndSeccomp(t *testing.T) {
	if os.Getenv("GLIDER_SECURITY_HELPER") == "1" {
		if err := ApplyDefault(); err != nil {
			t.Fatalf("ApplyDefault: %v", err)
		}
		status, err := os.ReadFile("/proc/self/status")
		if err != nil {
			t.Fatal(err)
		}
		text := string(status)
		if !strings.Contains(text, "NoNewPrivs:\t1") || !strings.Contains(text, "Seccomp:\t2") {
			t.Fatalf("policy not visible in status:\n%s", text)
		}
		if err := unix.Unshare(unix.CLONE_NEWNS); !errors.Is(err, syscall.EPERM) {
			t.Fatalf("unshare error=%v, want EPERM", err)
		}
		os.Exit(0)
	}
	if os.Geteuid() != 0 {
		t.Skip("capability reduction integration requires root")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestDefaultPolicyEnforcesNoNewPrivsAndSeccomp$")
	cmd.Env = append(os.Environ(), "GLIDER_SECURITY_HELPER=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("security helper: %v\n%s", err, out)
	}
}
