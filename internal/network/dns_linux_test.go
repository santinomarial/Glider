//go:build linux

package network

import (
	"net/netip"
	"os"
	"path/filepath"
	"testing"
)

func TestConfigureDNSWritesInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := ConfigureDNS(root, []netip.Addr{netip.MustParseAddr("9.9.9.9")}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "etc", "resolv.conf"))
	if err != nil || string(data) != "nameserver 9.9.9.9\n" {
		t.Fatalf("data=%q err=%v", data, err)
	}
}
func TestConfigureDNSRejectsEtcSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "etc")); err != nil {
		t.Fatal(err)
	}
	if err := ConfigureDNS(root, []netip.Addr{netip.MustParseAddr("9.9.9.9")}); err == nil {
		t.Fatal("accepted symlinked /etc")
	}
	if _, err := os.Stat(filepath.Join(outside, "resolv.conf")); !os.IsNotExist(err) {
		t.Fatalf("wrote outside root: %v", err)
	}
}
