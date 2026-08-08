//go:build linux

package network

import (
	"net/netip"
	"path/filepath"
	"testing"
)

func TestVethNamesDeterministicAndBounded(t *testing.T) {
	a, b := vethNames("workload-0/1")
	a2, b2 := vethNames("workload-0/1")
	if a != a2 || b != b2 || len(a) > 15 || len(b) > 15 || a == b {
		t.Fatalf("names %q %q / %q %q", a, b, a2, b2)
	}
}
func TestSafeOwner(t *testing.T) {
	for _, bad := range []string{"", "../x", "a/b", `a\b`, ".", ".."} {
		if safeOwner(bad) {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestPublishedPortConflictAcrossEndpoints(t *testing.T) {
	m, err := NewManager(filepath.Join(t.TempDir(), "network"), "10.64.0.0/24", DefaultBridge)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.save(Endpoint{Owner: "first", Address: netip.MustParseAddr("10.64.0.2"), Ports: []PortMapping{{Protocol: "tcp", HostPort: 8080, ContainerPort: 80}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.validatePortConflicts("second", []PortMapping{{Protocol: "tcp", HostPort: 8080, ContainerPort: 8080}}); err == nil {
		t.Fatal("expected published host-port conflict")
	}
	if err := m.validatePortConflicts("second", []PortMapping{{Protocol: "udp", HostPort: 8080, ContainerPort: 8080}}); err != nil {
		t.Fatalf("TCP and UDP ports should not conflict: %v", err)
	}
}

func TestValidateOverlayPeers(t *testing.T){valid:=[]Peer{{NodeID:"node-b",PodCIDR:netip.MustParsePrefix("10.64.2.0/24"),TunnelAddress:netip.MustParseAddr("192.0.2.2")}};if err:=validateOverlayPeers(valid);err!=nil{t.Fatal(err)};duplicates:=append(valid,Peer{NodeID:"node-c",PodCIDR:valid[0].PodCIDR,TunnelAddress:netip.MustParseAddr("192.0.2.3")});if err:=validateOverlayPeers(duplicates);err==nil{t.Fatal("accepted duplicate subnet")}}
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
