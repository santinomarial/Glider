//go:build linux && amd64

package security

import "testing"

func TestArchitecturePolicyBlocksLoadBearingSyscalls(t *testing.T) {
	arch, blocked, err := architecturePolicy(); if err != nil { t.Fatal(err) }
	if arch != 0xc000003e { t.Fatalf("arch=%#x",arch) }
	if len(blocked) < 10 { t.Fatalf("blocked only %d syscalls",len(blocked)) }
	seen:=map[uintptr]bool{};for _,nr:=range blocked{if seen[nr]{t.Fatalf("duplicate syscall %d",nr)};seen[nr]=true}
}
