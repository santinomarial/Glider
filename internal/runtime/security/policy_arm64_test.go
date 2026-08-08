//go:build linux && arm64

package security

import "testing"

func TestArm64ArchitecturePolicy(t *testing.T) {
	arch, blocked, err := architecturePolicy()
	if err != nil {
		t.Fatal(err)
	}
	if arch != 0xc00000b7 {
		t.Fatalf("arch=%#x", arch)
	}
	if len(blocked) < 10 {
		t.Fatalf("blocked only %d syscalls", len(blocked))
	}
}
