//go:build linux

package network

import "testing"

func TestVethNamesDeterministicAndBounded(t *testing.T) {
	a, b := vethNames("workload-0/1")
	a2, b2 := vethNames("workload-0/1")
	if a != a2 || b != b2 || len(a) > 15 || len(b) > 15 || a == b {
		t.Fatalf("names %q %q / %q %q", a, b, a2, b2)
	}
}
func TestSafeOwner(t *testing.T) {
	for _, bad := range []string{"", "../x", "a/b", ".", ".."} {
		if safeOwner(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}
