//go:build linux

package network

import (
	"context"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/nftables"
	"github.com/vishvananda/netlink"
)

func TestTwoNodeVXLANCarriesWorkloadTrafficAtValidatedMTU(t *testing.T) {
	connection := &nftables.Conn{}
	if _, err := connection.ListTables(); err != nil {
		t.Skipf("requires network-administration capability: %v", err)
	}
	for _, binary := range []string{"nsenter", "unshare"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is required", binary)
		}
	}
	hostA := networkNamespaceProcess(t)
	hostB := networkNamespaceProcess(t)
	connectUnderlay(t, hostA.Process.Pid, hostB.Process.Pid)
	runOverlayHelper(t, hostA.Process.Pid, map[string]string{"GLIDER_OVERLAY_MODE": "underlay", "GLIDER_OVERLAY_LINK": "under-a", "GLIDER_OVERLAY_ADDRESS": "192.0.2.1/24"})
	runOverlayHelper(t, hostB.Process.Pid, map[string]string{"GLIDER_OVERLAY_MODE": "underlay", "GLIDER_OVERLAY_LINK": "under-b", "GLIDER_OVERLAY_ADDRESS": "192.0.2.2/24"})

	workloadA := nestedNetworkProcess(t, hostA.Process.Pid)
	workloadB := nestedNetworkProcess(t, hostB.Process.Pid)
	rootA, rootB := filepath.Join(t.TempDir(), "node-a"), filepath.Join(t.TempDir(), "node-b")
	readyA, readyB := filepath.Join(t.TempDir(), "node-a-ready"), filepath.Join(t.TempDir(), "node-b-ready")
	runOverlayHelper(t, hostA.Process.Pid, map[string]string{"GLIDER_OVERLAY_MODE": "node", "GLIDER_OVERLAY_ROOT": rootA, "GLIDER_OVERLAY_CIDR": "10.64.1.0/24", "GLIDER_OVERLAY_OWNER": "workload-a", "GLIDER_OVERLAY_WORKLOAD_PID": strconv.Itoa(workloadA.Process.Pid), "GLIDER_OVERLAY_LOCAL": "192.0.2.1", "GLIDER_OVERLAY_PEER_ID": "node-b", "GLIDER_OVERLAY_PEER_CIDR": "10.64.2.0/24", "GLIDER_OVERLAY_PEER_TUNNEL": "192.0.2.2", "GLIDER_OVERLAY_READY": readyA})
	runOverlayHelper(t, hostB.Process.Pid, map[string]string{"GLIDER_OVERLAY_MODE": "node", "GLIDER_OVERLAY_ROOT": rootB, "GLIDER_OVERLAY_CIDR": "10.64.2.0/24", "GLIDER_OVERLAY_OWNER": "workload-b", "GLIDER_OVERLAY_WORKLOAD_PID": strconv.Itoa(workloadB.Process.Pid), "GLIDER_OVERLAY_LOCAL": "192.0.2.2", "GLIDER_OVERLAY_PEER_ID": "node-a", "GLIDER_OVERLAY_PEER_CIDR": "10.64.1.0/24", "GLIDER_OVERLAY_PEER_TUNNEL": "192.0.2.1", "GLIDER_OVERLAY_READY": readyB})

	addressA := strings.TrimSpace(readFile(t, readyA))
	addressB := strings.TrimSpace(readFile(t, readyB))
	if addressA != "10.64.1.2" || addressB != "10.64.2.2" {
		t.Fatalf("workload addresses = %s, %s", addressA, addressB)
	}
	backendReady := filepath.Join(t.TempDir(), "overlay-backend-ready")
	backend := namespaceHelper(workloadB.Process.Pid, "server", addressB+":19091", backendReady)
	if err := backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Process.Kill(); _ = backend.Wait() })
	waitForFile(t, backendReady)
	assertNamespaceDial(t, workloadA.Process.Pid, addressB+":19091", true)
	runOverlayHelper(t, hostA.Process.Pid, map[string]string{"GLIDER_OVERLAY_MODE": "service", "GLIDER_OVERLAY_ROOT": rootA, "GLIDER_OVERLAY_CIDR": "10.64.1.0/24", "GLIDER_OVERLAY_BACKEND": addressB})
	assertNamespaceDial(t, workloadA.Process.Pid, "10.96.0.55:18081", true)
}

func TestOverlayNamespaceHelper(t *testing.T) {
	mode := os.Getenv("GLIDER_OVERLAY_MODE")
	if mode == "" {
		t.Skip("helper process")
	}
	if mode == "underlay" {
		link, err := netlink.LinkByName(os.Getenv("GLIDER_OVERLAY_LINK"))
		if err != nil {
			t.Fatal(err)
		}
		address, err := netlink.ParseAddr(os.Getenv("GLIDER_OVERLAY_ADDRESS"))
		if err != nil {
			t.Fatal(err)
		}
		if err = netlink.AddrReplace(link, address); err != nil {
			t.Fatal(err)
		}
		if err = netlink.LinkSetUp(link); err != nil {
			t.Fatal(err)
		}
		if loopback, lookupErr := netlink.LinkByName("lo"); lookupErr == nil {
			_ = netlink.LinkSetUp(loopback)
		}
		return
	}
	if mode == "service" {
		manager, err := NewManager(os.Getenv("GLIDER_OVERLAY_ROOT"), os.Getenv("GLIDER_OVERLAY_CIDR"), DefaultBridge)
		if err != nil {
			t.Fatal(err)
		}
		service := Service{ID: "cross-node", ClusterIP: netip.MustParseAddr("10.96.0.55"), Port: 18081, Endpoints: []ServiceEndpoint{{Address: netip.MustParseAddr(os.Getenv("GLIDER_OVERLAY_BACKEND")), Port: 19091}}}
		if err = manager.EnsureServices([]Service{service}); err != nil {
			t.Fatal(err)
		}
		return
	}
	manager, err := NewManager(os.Getenv("GLIDER_OVERLAY_ROOT"), os.Getenv("GLIDER_OVERLAY_CIDR"), DefaultBridge)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(os.Getenv("GLIDER_OVERLAY_WORKLOAD_PID"))
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := manager.Ensure(context.Background(), os.Getenv("GLIDER_OVERLAY_OWNER"), pid)
	if err != nil {
		t.Fatal(err)
	}
	local := netip.MustParseAddr(os.Getenv("GLIDER_OVERLAY_LOCAL"))
	peer := Peer{NodeID: os.Getenv("GLIDER_OVERLAY_PEER_ID"), PodCIDR: netip.MustParsePrefix(os.Getenv("GLIDER_OVERLAY_PEER_CIDR")), TunnelAddress: netip.MustParseAddr(os.Getenv("GLIDER_OVERLAY_PEER_TUNNEL"))}
	if err = manager.EnsureOverlay(local, []Peer{peer}, 1450); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{DefaultBridge, DefaultVXLAN} {
		link, lookupErr := netlink.LinkByName(name)
		if lookupErr != nil || link.Attrs().MTU != 1450 {
			t.Fatalf("%s MTU = %v, %v", name, link, lookupErr)
		}
	}
	if err = os.WriteFile(os.Getenv("GLIDER_OVERLAY_READY"), []byte(endpoint.Address.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func connectUnderlay(t *testing.T, pidA, pidB int) {
	t.Helper()
	attributes := netlink.NewLinkAttrs()
	attributes.Name = "under-a"
	if err := netlink.LinkAdd(&netlink.Veth{LinkAttrs: attributes, PeerName: "under-b"}); err != nil {
		t.Fatal(err)
	}
	linkA, err := netlink.LinkByName("under-a")
	if err != nil {
		t.Fatal(err)
	}
	linkB, err := netlink.LinkByName("under-b")
	if err != nil {
		t.Fatal(err)
	}
	if err = netlink.LinkSetNsPid(linkA, pidA); err != nil {
		t.Fatal(err)
	}
	if err = netlink.LinkSetNsPid(linkB, pidB); err != nil {
		t.Fatal(err)
	}
}

func nestedNetworkProcess(t *testing.T, hostPID int) *exec.Cmd {
	t.Helper()
	command := exec.Command("nsenter", "-t", fmt.Sprint(hostPID), "-n", "unshare", "-n", "/bin/sleep", "60")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	return command
}

func runOverlayHelper(t *testing.T, hostPID int, environment map[string]string) {
	t.Helper()
	command := exec.Command("nsenter", "-t", fmt.Sprint(hostPID), "-n", os.Args[0], "-test.run=^TestOverlayNamespaceHelper$")
	command.Env = os.Environ()
	for key, value := range environment {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("overlay helper: %v: %s", err, output)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("overlay node did not become ready")
	return ""
}
