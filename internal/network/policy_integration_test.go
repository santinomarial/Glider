//go:build linux

package network

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/google/nftables"
	"golang.org/x/sys/unix"
)

func TestIngressAndEgressPolicyEnforceRealNamespaceTraffic(t *testing.T) {
	connection := &nftables.Conn{}
	if _, err := connection.ListTables(); err != nil {
		t.Skipf("requires network-administration capability: %v", err)
	}
	if _, err := exec.LookPath("nsenter"); err != nil {
		t.Skip("nsenter is required for the privileged packet test")
	}
	clientProcess := networkNamespaceProcess(t)
	serverProcess := networkNamespaceProcess(t)
	manager, err := NewManager(filepath.Join(t.TempDir(), "network"), "10.64.0.0/24", DefaultBridge)
	if err != nil {
		t.Fatal(err)
	}
	clientPolicy := NetworkPolicy{DefaultDenyEgress: true, Egress: []NetworkRule{{CIDR: "10.64.0.3/32", Protocol: "tcp", Ports: []uint16{19090}}}}
	serverPolicy := NetworkPolicy{DefaultDenyIngress: true, Ingress: []NetworkRule{{CIDR: "10.64.0.2/32", Protocol: "tcp", Ports: []uint16{19090}}}}
	clientEndpoint, err := manager.EnsureWithPolicy(context.Background(), "policy-client", clientProcess.Process.Pid, nil, clientPolicy)
	if err != nil {
		t.Fatal(err)
	}
	serverEndpoint, err := manager.EnsureWithPolicy(context.Background(), "policy-server", serverProcess.Process.Pid, nil, serverPolicy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Remove("policy-client"); _ = manager.Remove("policy-server") })
	if clientEndpoint.Address.String() != "10.64.0.2" || serverEndpoint.Address.String() != "10.64.0.3" {
		t.Fatalf("unexpected policy test addresses: %s %s", clientEndpoint.Address, serverEndpoint.Address)
	}
	ready := filepath.Join(t.TempDir(), "server-ready")
	backend := namespaceHelper(serverProcess.Process.Pid, "server", serverEndpoint.Address.String()+":19090", ready)
	if err = backend.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = backend.Process.Kill(); _ = backend.Wait() })
	waitForFile(t, ready)
	assertNamespaceDial(t, clientProcess.Process.Pid, serverEndpoint.Address.String()+":19090", true)

	if _, err = manager.EnsureWithPolicy(context.Background(), "policy-client", clientProcess.Process.Pid, nil, NetworkPolicy{DefaultDenyEgress: true}); err != nil {
		t.Fatal(err)
	}
	assertNamespaceDial(t, clientProcess.Process.Pid, serverEndpoint.Address.String()+":19090", false)

	if _, err = manager.EnsureWithPolicy(context.Background(), "policy-client", clientProcess.Process.Pid, nil, clientPolicy); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.EnsureWithPolicy(context.Background(), "policy-server", serverProcess.Process.Pid, nil, NetworkPolicy{DefaultDenyIngress: true}); err != nil {
		t.Fatal(err)
	}
	assertNamespaceDial(t, clientProcess.Process.Pid, serverEndpoint.Address.String()+":19090", false)

	if _, err = manager.EnsureWithPolicy(context.Background(), "policy-server", serverProcess.Process.Pid, nil, NetworkPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.EnsureWithPolicy(context.Background(), "policy-client", clientProcess.Process.Pid, nil, NetworkPolicy{DefaultDenyEgress: true}); err != nil {
		t.Fatal(err)
	}
	deleteGliderTable(t)
	assertNamespaceDial(t, clientProcess.Process.Pid, serverEndpoint.Address.String()+":19090", true)
	if err = manager.EnsureServices(nil); err != nil {
		t.Fatal(err)
	}
	assertNamespaceDial(t, clientProcess.Process.Pid, serverEndpoint.Address.String()+":19090", false)
}

func deleteGliderTable(t *testing.T) {
	t.Helper()
	connection := &nftables.Conn{}
	tables, err := connection.ListTables()
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table.Family == nftables.TableFamilyIPv4 && table.Name == nftTableName {
			connection.DelTable(table)
		}
	}
	if err = connection.Flush(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkPolicyNamespaceHelper(t *testing.T) {
	mode := os.Getenv("GLIDER_POLICY_HELPER")
	if mode == "" {
		t.Skip("helper process")
	}
	address := os.Getenv("GLIDER_POLICY_ADDRESS")
	if mode == "client" {
		connection, err := net.DialTimeout("tcp4", address, 750*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		data, err := io.ReadAll(connection)
		if err != nil || string(data) != "ok" {
			t.Fatalf("response=%q err=%v", data, err)
		}
		return
	}
	listener, err := net.Listen("tcp4", address)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if err = os.WriteFile(os.Getenv("GLIDER_POLICY_READY"), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	for {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_, _ = connection.Write([]byte("ok"))
		_ = connection.Close()
	}
}

func networkNamespaceProcess(t *testing.T) *exec.Cmd {
	t.Helper()
	command := exec.Command("/bin/sleep", "60")
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNET}
	if err := command.Start(); err != nil {
		t.Fatalf("create network namespace: %v", err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })
	return command
}

func namespaceHelper(pid int, mode, address, ready string) *exec.Cmd {
	command := exec.Command("nsenter", "-t", fmt.Sprint(pid), "-n", os.Args[0], "-test.run=^TestNetworkPolicyNamespaceHelper$")
	command.Env = append(os.Environ(), "GLIDER_POLICY_HELPER="+mode, "GLIDER_POLICY_ADDRESS="+address, "GLIDER_POLICY_READY="+ready)
	return command
}

func assertNamespaceDial(t *testing.T, pid int, address string, allowed bool) {
	t.Helper()
	command := namespaceHelper(pid, "client", address, "")
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if allowed && err != nil {
		t.Fatalf("allowed policy flow failed: %v: %s", err, output.String())
	}
	if !allowed && err == nil {
		t.Fatal("default-deny policy allowed flow")
	}
}

func waitForFile(t *testing.T, path string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("namespace backend did not become ready")
}
