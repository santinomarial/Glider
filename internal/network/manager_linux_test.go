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
func TestValidatePorts(t *testing.T) {
	if err := validatePorts([]PortMapping{{Protocol: "tcp", HostPort: 8080, ContainerPort: 80}, {Protocol: "udp", HostPort: 5353, ContainerPort: 53}}); err != nil {
		t.Fatal(err)
	}
	for _, ports := range [][]PortMapping{{{Protocol: "sctp", HostPort: 1, ContainerPort: 1}}, {{Protocol: "tcp", HostPort: 0, ContainerPort: 1}}, {{Protocol: "tcp", HostPort: 80, ContainerPort: 1}, {Protocol: "tcp", HostPort: 80, ContainerPort: 2}}} {
		if err := validatePorts(ports); err == nil {
			t.Fatalf("accepted %+v", ports)
		}
	}
}
func TestParsePortMapping(t *testing.T) {
	cases := map[string]PortMapping{"8080:80": {Protocol: "tcp", HostPort: 8080, ContainerPort: 80}, "5353:53/udp": {Protocol: "udp", HostPort: 5353, ContainerPort: 53}}
	for input, want := range cases {
		got, err := ParsePortMapping(input)
		if err != nil || got != want {
			t.Errorf("ParsePortMapping(%q)=%+v,%v want %+v", input, got, err, want)
		}
	}
	for _, bad := range []string{"80", "x:1", "1:0", "1:2/sctp"} {
		if _, err := ParsePortMapping(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}
