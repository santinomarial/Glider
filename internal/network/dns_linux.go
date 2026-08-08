//go:build linux

package network

import (
	"bufio"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func HostNameservers() []netip.Addr {
	f, err := os.Open("/etc/resolv.conf")
	if err != nil {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	defer f.Close()
	var out []netip.Addr
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 && fields[0] == "nameserver" {
			if addr, err := netip.ParseAddr(fields[1]); err == nil && !addr.IsLoopback() && !addr.IsUnspecified() {
				out = append(out, addr)
			}
		}
	}
	if len(out) == 0 {
		return []netip.Addr{netip.MustParseAddr("1.1.1.1")}
	}
	return out
}

// ConfigureDNS writes resolv.conf beneath rootfs without following an image
// supplied /etc or resolv.conf symlink outside the snapshot.
func ConfigureDNS(rootfs string, servers []netip.Addr) error {
	if rootfs == "" || len(servers) == 0 {
		return errors.New("rootfs and DNS servers are required")
	}
	rootFD, err := unix.Open(rootfs, unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(rootFD)
	etcFD, err := unix.Openat(rootFD, "etc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		if err := unix.Mkdirat(rootFD, "etc", 0o755); err != nil {
			return err
		}
		etcFD, err = unix.Openat(rootFD, "etc", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	}
	if err != nil {
		return fmt.Errorf("open rootfs /etc without symlinks: %w", err)
	}
	defer unix.Close(etcFD)
	fd, err := unix.Openat(etcFD, "resolv.conf", unix.O_WRONLY|unix.O_CREAT|unix.O_TRUNC|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o644)
	if err != nil {
		return fmt.Errorf("open rootfs resolv.conf: %w", err)
	}
	f := os.NewFile(uintptr(fd), "resolv.conf")
	for _, addr := range servers {
		if !addr.IsValid() {
			f.Close()
			return errors.New("invalid DNS server")
		}
		if _, err := fmt.Fprintf(f, "nameserver %s\n", addr); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
