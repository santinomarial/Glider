//go:build linux

package network

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"

	"github.com/google/nftables"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

const nftTableName = "glider"

// reconcileNAT replaces only Glider's nftables table from durable endpoint
// records. It is safe after crashes and manual rule deletion.
func (m *Manager) reconcileNAT() error {
	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1\n"), 0o644); err != nil {
		return fmt.Errorf("enable IPv4 forwarding: %w", err)
	}
	endpoints, err := m.loadEndpoints()
	if err != nil {
		return err
	}
	conn := &nftables.Conn{}
	tables, err := conn.ListTables()
	if err != nil {
		return err
	}
	for _, table := range tables {
		if table.Family == nftables.TableFamilyIPv4 && table.Name == nftTableName {
			conn.DelTable(table)
		}
	}
	table := conn.AddTable(&nftables.Table{Family: nftables.TableFamilyIPv4, Name: nftTableName})
	post := conn.AddChain(&nftables.Chain{Name: "postrouting", Table: table, Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPostrouting, Priority: nftables.ChainPriorityNATSource})
	pre := conn.AddChain(&nftables.Chain{Name: "prerouting", Table: table, Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookPrerouting, Priority: nftables.ChainPriorityNATDest})
	output := conn.AddChain(&nftables.Chain{Name: "output", Table: table, Type: nftables.ChainTypeNAT, Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityNATDest})
	network := m.pool.Subnet().Masked().Addr().As4()
	maskBits := m.pool.Subnet().Bits()
	mask := binary.BigEndian.Uint32([]byte{255, 255, 255, 255}) << uint(32-maskBits)
	maskData := make([]byte, 4)
	binary.BigEndian.PutUint32(maskData, mask)
	conn.AddRule(&nftables.Rule{Table: table, Chain: post, Exprs: []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: 12, Len: 4}, &expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: maskData, Xor: []byte{0, 0, 0, 0}}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: network[:]}, &expr.Masq{}}})
	for _, ep := range endpoints {
		if ep.Phase != "READY" {
			continue
		}
		for _, p := range ep.Ports {
			proto := byte(unix.IPPROTO_TCP)
			if p.Protocol == "udp" {
				proto = unix.IPPROTO_UDP
			}
			host := make([]byte, 2)
			binary.BigEndian.PutUint16(host, p.HostPort)
			container := make([]byte, 2)
			binary.BigEndian.PutUint16(container, p.ContainerPort)
			for _, chain := range []*nftables.Chain{pre, output} {
				conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{proto}}, &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: host}, &expr.Immediate{Register: 1, Data: ep.Address.AsSlice()}, &expr.Immediate{Register: 2, Data: container}, &expr.NAT{Type: expr.NATTypeDestNAT, Family: unix.NFPROTO_IPV4, RegAddrMin: 1, RegProtoMin: 2}}})
			}
		}
	}
	if err := conn.Flush(); err != nil {
		return fmt.Errorf("apply Glider nftables rules: %w", err)
	}
	return nil
}

func (m *Manager) loadEndpoints() ([]Endpoint, error) {
	entries, err := os.ReadDir(m.root + "/endpoints")
	if err != nil {
		return nil, err
	}
	out := make([]Endpoint, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || len(entry.Name()) < 6 || entry.Name()[len(entry.Name())-5:] != ".json" {
			continue
		}
		data, err := os.ReadFile(m.root + "/endpoints/" + entry.Name())
		if err != nil {
			return nil, err
		}
		var ep Endpoint
		if err := json.Unmarshal(data, &ep); err != nil {
			return nil, fmt.Errorf("decode endpoint %s: %w", entry.Name(), err)
		}
		out = append(out, ep)
	}
	return out, nil
}
