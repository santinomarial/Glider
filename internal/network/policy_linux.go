//go:build linux

package network

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"

	"github.com/google/nftables"
	"github.com/google/nftables/binaryutil"
	"github.com/google/nftables/expr"
	"golang.org/x/sys/unix"
)

type NetworkPolicy struct {
	DefaultDenyIngress bool          `json:"default_deny_ingress,omitempty"`
	DefaultDenyEgress  bool          `json:"default_deny_egress,omitempty"`
	Ingress            []NetworkRule `json:"ingress,omitempty"`
	Egress             []NetworkRule `json:"egress,omitempty"`
}

type NetworkRule struct {
	CIDR     string   `json:"cidr"`
	Protocol string   `json:"protocol,omitempty"`
	Ports    []uint16 `json:"ports,omitempty"`
}

func validateNetworkPolicy(policy NetworkPolicy) error {
	if len(policy.Ingress) > 64 || len(policy.Egress) > 64 {
		return errors.New("network policy exceeds rule limit")
	}
	for _, rule := range append(append([]NetworkRule(nil), policy.Ingress...), policy.Egress...) {
		prefix, err := netip.ParsePrefix(rule.CIDR)
		if err != nil || !prefix.Addr().Is4() || prefix != prefix.Masked() {
			return fmt.Errorf("invalid canonical IPv4 policy CIDR %q", rule.CIDR)
		}
		if rule.Protocol != "" && rule.Protocol != "tcp" && rule.Protocol != "udp" && rule.Protocol != "icmp" {
			return fmt.Errorf("invalid policy protocol %q", rule.Protocol)
		}
		if len(rule.Ports) > 64 || (rule.Protocol != "tcp" && rule.Protocol != "udp") && len(rule.Ports) > 0 {
			return errors.New("policy ports require tcp or udp and are limited to 64")
		}
		seen := map[uint16]bool{}
		for _, port := range rule.Ports {
			if port == 0 || seen[port] {
				return errors.New("policy ports must be unique and non-zero")
			}
			seen[port] = true
		}
	}
	return nil
}

func addPolicyRules(conn *nftables.Conn, table *nftables.Table, forward *nftables.Chain, endpoints []Endpoint) {
	conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: []expr.Any{
		&expr.Ct{Register: 1, Key: expr.CtKeySTATE},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(expr.CtStateBitESTABLISHED | expr.CtStateBitRELATED), Xor: binaryutil.NativeEndian.PutUint32(0)},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Verdict{Kind: expr.VerdictAccept},
	}})
	for _, endpoint := range endpoints {
		if endpoint.Phase != "READY" || !endpoint.Address.Is4() {
			continue
		}
		if endpoint.Policy.DefaultDenyEgress {
			chain := conn.AddChain(&nftables.Chain{Name: policyChainName("eg", endpoint.Owner), Table: table})
			conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: append(addressMatch(12, endpoint.Address), &expr.Verdict{Kind: expr.VerdictJump, Chain: chain.Name})})
			addDirectionRules(conn, table, chain, endpoint.Policy.Egress, 16)
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}})
		}
		if endpoint.Policy.DefaultDenyIngress {
			chain := conn.AddChain(&nftables.Chain{Name: policyChainName("in", endpoint.Owner), Table: table})
			conn.AddRule(&nftables.Rule{Table: table, Chain: forward, Exprs: append(addressMatch(16, endpoint.Address), &expr.Verdict{Kind: expr.VerdictJump, Chain: chain.Name})})
			addDirectionRules(conn, table, chain, endpoint.Policy.Ingress, 12)
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: []expr.Any{&expr.Verdict{Kind: expr.VerdictDrop}}})
		}
	}
}

func addDirectionRules(conn *nftables.Conn, table *nftables.Table, chain *nftables.Chain, rules []NetworkRule, remoteOffset uint32) {
	for _, rule := range rules {
		prefix, err := netip.ParsePrefix(rule.CIDR)
		if err != nil {
			continue
		}
		base := prefixMatch(remoteOffset, prefix)
		protocol := protocolNumber(rule.Protocol)
		if protocol != 0 {
			base = append(base, &expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{protocol}})
		}
		if len(rule.Ports) == 0 {
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: append(append([]expr.Any(nil), base...), &expr.Verdict{Kind: expr.VerdictReturn})})
			continue
		}
		for _, value := range rule.Ports {
			port := make([]byte, 2)
			binary.BigEndian.PutUint16(port, value)
			expressions := append(append([]expr.Any(nil), base...), &expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: 2, Len: 2}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: port}, &expr.Verdict{Kind: expr.VerdictReturn})
			conn.AddRule(&nftables.Rule{Table: table, Chain: chain, Exprs: expressions})
		}
	}
}

func addressMatch(offset uint32, address netip.Addr) []expr.Any {
	return []expr.Any{&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4}, &expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: address.AsSlice()}}
}

func prefixMatch(offset uint32, prefix netip.Prefix) []expr.Any {
	maskBits := prefix.Bits()
	mask := uint32(0)
	if maskBits > 0 {
		mask = ^uint32(0) << uint(32-maskBits)
	}
	maskData := make([]byte, 4)
	binary.BigEndian.PutUint32(maskData, mask)
	return []expr.Any{
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseNetworkHeader, Offset: offset, Len: 4},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: maskData, Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: prefix.Masked().Addr().AsSlice()},
	}
}

func protocolNumber(protocol string) byte {
	switch protocol {
	case "tcp":
		return unix.IPPROTO_TCP
	case "udp":
		return unix.IPPROTO_UDP
	case "icmp":
		return unix.IPPROTO_ICMP
	default:
		return 0
	}
}

func policyChainName(direction, owner string) string {
	sum := sha256.Sum256([]byte(owner))
	return fmt.Sprintf("%s-%x", direction, sum[:6])
}
