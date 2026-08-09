//go:build linux

// Package network owns Glider's node-local bridge/veth/network-namespace
// lifecycle. Kernel configuration uses netlink directly, never shell commands.
package network

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"

	"github.com/santinomarial/glider/internal/network/ipam"
)

const DefaultBridge = "glider0"

type Endpoint struct {
	Owner    string        `json:"owner"`
	Address  netip.Addr    `json:"address"`
	Gateway  netip.Addr    `json:"gateway"`
	HostVeth string        `json:"host_veth"`
	Phase    string        `json:"phase"`
	Ports    []PortMapping `json:"ports,omitempty"`
	Policy   NetworkPolicy `json:"policy,omitempty"`
}
type PortMapping struct {
	Protocol      string `json:"protocol"`
	HostPort      uint16 `json:"host_port"`
	ContainerPort uint16 `json:"container_port"`
}
type Manager struct {
	root, bridge string
	pool         *ipam.Pool
}

func NewManager(root, cidr, bridge string) (*Manager, error) {
	if root == "" || !filepath.IsAbs(root) {
		return nil, errors.New("network state root must be absolute")
	}
	if bridge == "" {
		bridge = DefaultBridge
	}
	if len(bridge) > 15 {
		return nil, errors.New("bridge name exceeds Linux IFNAMSIZ")
	}
	if err := os.MkdirAll(filepath.Join(root, "endpoints"), 0o755); err != nil {
		return nil, err
	}
	pool, err := ipam.Open(filepath.Join(root, "ipam"), cidr)
	if err != nil {
		return nil, err
	}
	return &Manager{root, bridge, pool}, nil
}

func (m *Manager) Ensure(ctx context.Context, owner string, initPID int) (Endpoint, error) {
	return m.EnsureWithPorts(ctx, owner, initPID, nil)
}
func (m *Manager) EnsureWithPorts(ctx context.Context, owner string, initPID int, ports []PortMapping) (Endpoint, error) {
	return m.EnsureWithPolicy(ctx, owner, initPID, ports, NetworkPolicy{})
}
func (m *Manager) EnsureWithPolicy(ctx context.Context, owner string, initPID int, ports []PortMapping, policy NetworkPolicy) (Endpoint, error) {
	if err := ctx.Err(); err != nil {
		return Endpoint{}, err
	}
	if !safeOwner(owner) || initPID <= 0 {
		return Endpoint{}, errors.New("invalid endpoint owner or PID")
	}
	unlock, err := m.lock()
	if err != nil {
		return Endpoint{}, err
	}
	defer unlock()
	if err := m.validatePortConflicts(owner, ports); err != nil {
		return Endpoint{}, err
	}
	if err := validateNetworkPolicy(policy); err != nil {
		return Endpoint{}, err
	}
	allocation, err := m.pool.Ensure(owner)
	if err != nil {
		return Endpoint{}, err
	}
	host, peer := vethNames(owner)
	if err := validatePorts(ports); err != nil {
		return Endpoint{}, err
	}
	ep := Endpoint{Owner: owner, Address: allocation.Address, Gateway: m.pool.Gateway(), HostVeth: host, Phase: "CREATING", Ports: append([]PortMapping(nil), ports...), Policy: policy}
	if err := m.save(ep); err != nil {
		_ = m.pool.Release(owner)
		return Endpoint{}, err
	}
	if err := m.ensureBridge(); err != nil {
		return Endpoint{}, err
	}
	ns, err := netns.GetFromPid(initPID)
	if err != nil {
		return Endpoint{}, fmt.Errorf("open container netns: %w", err)
	}
	defer ns.Close()
	handle, err := netlink.NewHandleAt(ns)
	if err != nil {
		return Endpoint{}, err
	}
	defer handle.Close()
	configured := false
	if link, err := handle.LinkByName("eth0"); err == nil {
		configured = hasAddress(handle, link, allocation.Address)
	}
	if !configured {
		if existing, err := netlink.LinkByName(host); err == nil {
			if err := netlink.LinkDel(existing); err != nil {
				return Endpoint{}, err
			}
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = host
		veth := &netlink.Veth{LinkAttrs: attrs, PeerName: peer}
		if err := netlink.LinkAdd(veth); err != nil {
			return Endpoint{}, fmt.Errorf("create veth: %w", err)
		}
		hostLink, _ := netlink.LinkByName(host)
		bridgeLink, _ := netlink.LinkByName(m.bridge)
		if err := netlink.LinkSetMaster(hostLink, bridgeLink); err != nil {
			return Endpoint{}, err
		}
		if err := netlink.LinkSetUp(hostLink); err != nil {
			return Endpoint{}, err
		}
		peerLink, err := netlink.LinkByName(peer)
		if err != nil {
			return Endpoint{}, err
		}
		if err := netlink.LinkSetNsFd(peerLink, int(ns)); err != nil {
			return Endpoint{}, err
		}
		peerLink, err = handle.LinkByName(peer)
		if err != nil {
			return Endpoint{}, err
		}
		if err := handle.LinkSetName(peerLink, "eth0"); err != nil {
			return Endpoint{}, err
		}
	}
	eth0, err := handle.LinkByName("eth0")
	if err != nil {
		return Endpoint{}, err
	}
	prefix := netip.PrefixFrom(allocation.Address, m.pool.Subnet().Bits())
	addr, _ := netlink.ParseAddr(prefix.String())
	if err := handle.AddrReplace(eth0, addr); err != nil {
		return Endpoint{}, err
	}
	if err := handle.LinkSetUp(eth0); err != nil {
		return Endpoint{}, err
	}
	if lo, err := handle.LinkByName("lo"); err == nil {
		_ = handle.LinkSetUp(lo)
	}
	route := &netlink.Route{LinkIndex: eth0.Attrs().Index, Gw: net.IP(m.pool.Gateway().AsSlice())}
	if err := handle.RouteReplace(route); err != nil {
		return Endpoint{}, err
	}
	ep.Phase = "READY"
	if err := m.save(ep); err != nil {
		return Endpoint{}, err
	}
	if err := m.reconcileNAT(); err != nil {
		return Endpoint{}, err
	}
	return ep, nil
}

func (m *Manager) Remove(owner string) error {
	if !safeOwner(owner) {
		return errors.New("invalid endpoint owner")
	}
	unlock, err := m.lock()
	if err != nil {
		return err
	}
	defer unlock()
	host, _ := vethNames(owner)
	if link, err := netlink.LinkByName(host); err == nil {
		if err := netlink.LinkDel(link); err != nil {
			return err
		}
	}
	err = os.Remove(m.path(owner))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := m.reconcileNAT(); err != nil {
		return err
	}
	return m.pool.Release(owner)
}
func (m *Manager) ensureBridge() error {
	link, err := netlink.LinkByName(m.bridge)
	if err != nil {
		if _, ok := err.(netlink.LinkNotFoundError); !ok {
			return err
		}
		attrs := netlink.NewLinkAttrs()
		attrs.Name = m.bridge
		if err := netlink.LinkAdd(&netlink.Bridge{LinkAttrs: attrs}); err != nil {
			return err
		}
		link, err = netlink.LinkByName(m.bridge)
		if err != nil {
			return err
		}
	}
	prefix := netip.PrefixFrom(m.pool.Gateway(), m.pool.Subnet().Bits())
	addr, _ := netlink.ParseAddr(prefix.String())
	if err := netlink.AddrReplace(link, addr); err != nil {
		return err
	}
	return netlink.LinkSetUp(link)
}
func hasAddress(h *netlink.Handle, l netlink.Link, want netip.Addr) bool {
	addrs, err := h.AddrList(l, netlink.FAMILY_V4)
	if err != nil {
		return false
	}
	for _, a := range addrs {
		if got, ok := netip.AddrFromSlice(a.IP); ok && got.Unmap() == want {
			return true
		}
	}
	return false
}
func vethNames(owner string) (string, string) {
	sum := sha256.Sum256([]byte(owner))
	suffix := hex.EncodeToString(sum[:5])
	return "gv" + suffix, "gp" + suffix
}
func safeOwner(s string) bool {
	return s != "" && len(s) <= 128 && filepath.Base(s) == s && s != "." && s != ".." && !strings.ContainsAny(s, "\\\x00")
}

func (m *Manager) lock() (func(), error) {
	f, err := os.OpenFile(filepath.Join(m.root, "network.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

func (m *Manager) validatePortConflicts(owner string, ports []PortMapping) error {
	if err := validatePorts(ports); err != nil {
		return err
	}
	wanted := make(map[string]struct{}, len(ports))
	for _, p := range ports {
		wanted[fmt.Sprintf("%s/%d", p.Protocol, p.HostPort)] = struct{}{}
	}
	endpoints, err := m.loadEndpoints()
	if err != nil {
		return err
	}
	for _, endpoint := range endpoints {
		if endpoint.Owner == owner {
			continue
		}
		for _, p := range endpoint.Ports {
			key := fmt.Sprintf("%s/%d", p.Protocol, p.HostPort)
			if _, exists := wanted[key]; exists {
				return fmt.Errorf("host port %s is already owned by %s", key, endpoint.Owner)
			}
		}
	}
	return nil
}
func validatePorts(ports []PortMapping) error {
	seen := map[string]bool{}
	for _, p := range ports {
		if p.Protocol != "tcp" && p.Protocol != "udp" {
			return fmt.Errorf("unsupported port protocol %q", p.Protocol)
		}
		if p.HostPort == 0 || p.ContainerPort == 0 {
			return errors.New("ports must be nonzero")
		}
		key := fmt.Sprintf("%s/%d", p.Protocol, p.HostPort)
		if seen[key] {
			return fmt.Errorf("duplicate host port %s", key)
		}
		seen[key] = true
	}
	return nil
}

func ParsePortMapping(value string) (PortMapping, error) {
	var p PortMapping
	p.Protocol = "tcp"
	main, protocol, hasProtocol := strings.Cut(value, "/")
	if hasProtocol {
		p.Protocol = strings.ToLower(protocol)
	}
	host, container, ok := strings.Cut(main, ":")
	if !ok {
		return p, fmt.Errorf("port mapping %q must be HOST:CONTAINER[/tcp|udp]", value)
	}
	h, err := strconv.ParseUint(host, 10, 16)
	if err != nil {
		return p, fmt.Errorf("invalid host port: %w", err)
	}
	c, err := strconv.ParseUint(container, 10, 16)
	if err != nil {
		return p, fmt.Errorf("invalid container port: %w", err)
	}
	p.HostPort = uint16(h)
	p.ContainerPort = uint16(c)
	if err := validatePorts([]PortMapping{p}); err != nil {
		return p, err
	}
	return p, nil
}
func (m *Manager) path(owner string) string { return filepath.Join(m.root, "endpoints", owner+".json") }
func (m *Manager) Endpoint(owner string) (Endpoint, error) {
	if !safeOwner(owner) {
		return Endpoint{}, errors.New("invalid endpoint owner")
	}
	data, err := os.ReadFile(m.path(owner))
	if err != nil {
		return Endpoint{}, err
	}
	var endpoint Endpoint
	if err := json.Unmarshal(data, &endpoint); err != nil {
		return Endpoint{}, err
	}
	if endpoint.Owner != owner {
		return Endpoint{}, errors.New("endpoint owner mismatch")
	}
	return endpoint, nil
}
func (m *Manager) save(ep Endpoint) error {
	data, err := json.MarshalIndent(ep, "", "  ")
	if err != nil {
		return err
	}
	final := m.path(ep.Owner)
	tmp := final + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp, final); err != nil {
		return err
	}
	if d, err := os.Open(filepath.Dir(final)); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}
