//go:build linux

package network

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

const DefaultVXLAN = "glider-vxlan"
const gliderRouteProtocol = 99
type Peer struct { NodeID string `json:"node_id"`; PodCIDR netip.Prefix `json:"pod_cidr"`; TunnelAddress netip.Addr `json:"tunnel_address"` }

// EnsureOverlay level-triggers the VXLAN device, flood entries, and remote
// pod-subnet routes from the complete peer snapshot. Stale Glider routes are
// removed before desired routes are replaced.
func (m *Manager) EnsureOverlay(localTunnel netip.Addr, peers []Peer, mtu int) error {
	if !localTunnel.Is4(){return errors.New("local tunnel address must be IPv4")};if mtu==0{mtu=1450};if mtu<576||mtu>9000{return errors.New("invalid overlay MTU")}
	unlock,err:=m.lock();if err!=nil{return err};defer unlock()
	if err:=m.ensureBridge();err!=nil{return err}
	bridge,err:=netlink.LinkByName(m.bridge);if err!=nil{return err};_ = netlink.LinkSetMTU(bridge,mtu)
	vx,err:=netlink.LinkByName(DefaultVXLAN)
	if err!=nil{
		if _,ok:=err.(netlink.LinkNotFoundError);!ok{return err}
		attrs:=netlink.NewLinkAttrs();attrs.Name=DefaultVXLAN;attrs.MTU=mtu
		device:=&netlink.Vxlan{LinkAttrs:attrs,VxlanId:64,SrcAddr:net.IP(localTunnel.AsSlice()),Port:4789,Learning:true}
		if err:=netlink.LinkAdd(device);err!=nil{return fmt.Errorf("create VXLAN: %w",err)};vx,err=netlink.LinkByName(DefaultVXLAN);if err!=nil{return err}
	}
	if err:=netlink.LinkSetMTU(vx,mtu);err!=nil{return err};if err:=netlink.LinkSetMaster(vx,bridge);err!=nil{return err};if err:=netlink.LinkSetUp(vx);err!=nil{return err}
	routes,err:=netlink.RouteList(bridge,netlink.FAMILY_V4);if err!=nil{return err};for _,route:=range routes{if route.Protocol==gliderRouteProtocol{_ = netlink.RouteDel(&route)}}
	neighbors, _ := netlink.NeighList(vx.Attrs().Index, netlink.FAMILY_V4);for _,n:=range neighbors{if n.Flags&netlink.NTF_SELF!=0{_ = netlink.NeighDel(&n)}}
	seen:=map[string]bool{}
	for _,peer:=range peers{
		if peer.NodeID==""||!peer.PodCIDR.IsValid()||!peer.PodCIDR.Addr().Is4()||!peer.TunnelAddress.Is4(){return fmt.Errorf("invalid overlay peer %q",peer.NodeID)}
		if seen[peer.PodCIDR.String()]{return fmt.Errorf("duplicate peer subnet %s",peer.PodCIDR)};seen[peer.PodCIDR.String()]=true
		dst:=net.IPNet{IP:net.IP(peer.PodCIDR.Masked().Addr().AsSlice()),Mask:net.CIDRMask(peer.PodCIDR.Bits(),32)}
		if err:=netlink.RouteReplace(&netlink.Route{LinkIndex:bridge.Attrs().Index,Dst:&dst,Protocol:gliderRouteProtocol,Scope:netlink.SCOPE_LINK});err!=nil{return err}
		flood:=&netlink.Neigh{LinkIndex:vx.Attrs().Index,Family:unix.AF_BRIDGE,State:netlink.NUD_PERMANENT,Flags:netlink.NTF_SELF,HardwareAddr:net.HardwareAddr{0,0,0,0,0,0},IP:net.IP(peer.TunnelAddress.AsSlice())}
		if err:=netlink.NeighAppend(flood);err!=nil&&!errors.Is(err,os.ErrExist){return fmt.Errorf("add VXLAN peer %s: %w",peer.NodeID,err)}
	}
	data,err:=json.MarshalIndent(peers,"","  ");if err!=nil{return err};return durableWrite(filepath.Join(m.root,"overlay-peers.json"),data)
}

func durableWrite(final string,data []byte)error{tmp:=final+".tmp";f,err:=os.OpenFile(tmp,os.O_CREATE|os.O_TRUNC|os.O_WRONLY,0o600);if err!=nil{return err};if _,err=f.Write(data);err==nil{err=f.Sync()};if closeErr:=f.Close();err==nil{err=closeErr};if err!=nil{return err};if err=os.Rename(tmp,final);err!=nil{return err};if d,openErr:=os.Open(filepath.Dir(final));openErr==nil{_ = d.Sync();_ = d.Close()};return nil}
