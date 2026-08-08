// Package ipam implements persistent, concurrency-safe node-local IPv4
// allocation. Allocation is durable before network resources are created.
package ipam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

var (
	ErrExhausted    = errors.New("IP address pool exhausted")
	ErrInvalidOwner = errors.New("invalid IPAM owner")
)

type Allocation struct {
	Owner   string     `json:"owner"`
	Address netip.Addr `json:"address"`
}
type state struct {
	Version     int               `json:"version"`
	Subnet      string            `json:"subnet"`
	Allocations map[string]string `json:"allocations"`
}
type Pool struct {
	dir     string
	subnet  netip.Prefix
	gateway netip.Addr
}

func Open(dir, cidr string) (*Pool, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, errors.New("IPAM directory must be absolute")
	}
	prefix, err := netip.ParsePrefix(cidr)
	if err != nil || !prefix.Addr().Is4() {
		return nil, fmt.Errorf("invalid IPv4 subnet %q", cidr)
	}
	prefix = prefix.Masked()
	if prefix.Bits() > 30 {
		return nil, fmt.Errorf("subnet %s has no usable container addresses", prefix)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Pool{dir: dir, subnet: prefix, gateway: prefix.Addr().Next()}, nil
}

func (p *Pool) Gateway() netip.Addr  { return p.gateway }
func (p *Pool) Subnet() netip.Prefix { return p.subnet }

// Ensure returns owner's existing address or durably reserves the lowest free
// address. Concurrent processes serialize through flock.
func (p *Pool) Ensure(owner string) (Allocation, error) {
	if !validOwner(owner) {
		return Allocation{}, ErrInvalidOwner
	}
	lock, err := p.lock()
	if err != nil {
		return Allocation{}, err
	}
	defer lock.close()
	s, err := p.load()
	if err != nil {
		return Allocation{}, err
	}
	if raw, ok := s.Allocations[owner]; ok {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return Allocation{}, fmt.Errorf("corrupt IPAM address for %s", owner)
		}
		return Allocation{owner, addr}, nil
	}
	used := make(map[netip.Addr]bool, len(s.Allocations))
	for _, raw := range s.Allocations {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return Allocation{}, fmt.Errorf("corrupt IPAM state address %q", raw)
		}
		used[addr] = true
	}
	last := lastAddress(p.subnet)
	for addr := p.gateway.Next(); addr.IsValid() && addr.Compare(last) < 0; addr = addr.Next() {
		if !used[addr] {
			s.Allocations[owner] = addr.String()
			if err := p.save(s); err != nil {
				return Allocation{}, err
			}
			return Allocation{owner, addr}, nil
		}
	}
	return Allocation{}, ErrExhausted
}

func (p *Pool) Release(owner string) error {
	if !validOwner(owner) {
		return ErrInvalidOwner
	}
	lock, err := p.lock()
	if err != nil {
		return err
	}
	defer lock.close()
	s, err := p.load()
	if err != nil {
		return err
	}
	if _, ok := s.Allocations[owner]; !ok {
		return nil
	}
	delete(s.Allocations, owner)
	return p.save(s)
}

func (p *Pool) List() ([]Allocation, error) {
	lock, err := p.lock()
	if err != nil {
		return nil, err
	}
	defer lock.close()
	s, err := p.load()
	if err != nil {
		return nil, err
	}
	out := make([]Allocation, 0, len(s.Allocations))
	for owner, raw := range s.Allocations {
		addr, err := netip.ParseAddr(raw)
		if err != nil {
			return nil, fmt.Errorf("corrupt IPAM address %q", raw)
		}
		out = append(out, Allocation{owner, addr})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Owner < out[j].Owner })
	return out, nil
}

func (p *Pool) load() (state, error) {
	path := filepath.Join(p.dir, "state.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return state{Version: 1, Subnet: p.subnet.String(), Allocations: map[string]string{}}, nil
	}
	if err != nil {
		return state{}, err
	}
	var s state
	if err := json.Unmarshal(data, &s); err != nil {
		return state{}, fmt.Errorf("decode IPAM state: %w", err)
	}
	if s.Version != 1 || s.Subnet != p.subnet.String() || s.Allocations == nil {
		return state{}, errors.New("IPAM state does not match configured subnet or version")
	}
	return s, nil
}
func (p *Pool) save(s state) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(p.dir, "state.json.tmp")
	final := filepath.Join(p.dir, "state.json")
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
	if d, err := os.Open(p.dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
	return nil
}

type fileLock struct{ f *os.File }

func (p *Pool) lock() (*fileLock, error) {
	f, err := os.OpenFile(filepath.Join(p.dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &fileLock{f}, nil
}
func (l *fileLock) close() { _ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN); _ = l.f.Close() }
func validOwner(s string) bool {
	if len(s) == 0 || len(s) > 256 {
		return false
	}
	for _, r := range s {
		if r == '/' || r == '\\' || r == '\x00' {
			return false
		}
	}
	return true
}
func lastAddress(p netip.Prefix) netip.Addr {
	bits := p.Addr().As4()
	host := uint(32 - p.Bits())
	mask := uint32(1<<host) - 1
	n := uint32(bits[0])<<24 | uint32(bits[1])<<16 | uint32(bits[2])<<8 | uint32(bits[3])
	n |= mask
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)})
}
