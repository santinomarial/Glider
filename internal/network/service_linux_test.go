//go:build linux

package network

import (
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/nftables"
)

func TestServiceVirtualIPForwardsRealTCPConnection(t *testing.T) {
	connection := &nftables.Conn{}
	if _, err := connection.ListTables(); err != nil {
		t.Skipf("requires network-administration capability: %v", err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	backendPort := uint16(listener.Addr().(*net.TCPAddr).Port)
	go func() {
		backend, acceptErr := listener.Accept()
		if acceptErr == nil {
			_, _ = backend.Write([]byte("ok"))
			_ = backend.Close()
		}
	}()

	manager, err := NewManager(filepath.Join(t.TempDir(), "network"), "10.64.0.0/24", DefaultBridge)
	if err != nil {
		t.Fatal(err)
	}
	service := Service{ID: "api", ClusterIP: netip.MustParseAddr("10.96.0.42"), Port: 18080, Endpoints: []ServiceEndpoint{{Address: netip.MustParseAddr("127.0.0.1"), Port: backendPort}}}
	if err = manager.EnsureServices([]Service{service}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.EnsureServices(nil) })
	client, err := net.DialTimeout("tcp4", "10.96.0.42:18080", 3*time.Second)
	if err != nil {
		t.Fatalf("dial service virtual IP: %v", err)
	}
	defer client.Close()
	response, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(response) != "ok" {
		t.Fatalf("service response = %q", response)
	}
}
