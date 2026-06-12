// Package recipe holds the outcome-focused intents (docs/NET.md §4.3): a user
// thinks "add a guest network", not "create a wifi-iface + interface + zone +
// forwarding + dhcp pool". Each recipe is a pure function from the current
// Config + params to a ChangeSet whose output, applied to a sensible base,
// always passes validation (the recipe-soundness property, §9.2). ChangeSets are
// all-or-nothing and never mutate the input Config.
//
// IsolateDevice (block a device by MAC) is deferred until the firewall model
// gains src_mac/src_ip fields.
package recipe

import (
	"fmt"
	"net/netip"

	"github.com/sirmick/wash/internal/washnet/change"
	"github.com/sirmick/wash/internal/washnet/model"
)

// GuestNetwork creates an isolated guest wifi network: an interface, a firewall
// zone forwarded only to the WAN, a DHCP pool, and an AP SSID. The base must
// already have the named radio and WAN zone.
type GuestNetwork struct {
	SSID, Key string
	Radio     string
	Gateway   netip.Prefix // guest gateway/subnet, e.g. 192.168.20.1/24
	Interface string       // default "guest"
	Zone      string       // default "guest"
	WANZone   string       // default "wan"
	PoolStart int          // default 100
	PoolLimit int          // default 150
	LeaseTime string       // default "12h"
}

func AddGuestNetwork(_ model.Config, p GuestNetwork) (change.ChangeSet, error) {
	if p.SSID == "" || p.Key == "" || p.Radio == "" || !p.Gateway.IsValid() {
		return change.ChangeSet{}, fmt.Errorf("guest network needs SSID, Key, Radio, and a valid Gateway")
	}
	def(&p.Interface, "guest")
	def(&p.Zone, "guest")
	def(&p.WANZone, "wan")
	def(&p.LeaseTime, "12h")
	if p.PoolStart == 0 {
		p.PoolStart = 100
	}
	if p.PoolLimit == 0 {
		p.PoolLimit = 150
	}
	return change.ChangeSet{
		Summary: []string{
			fmt.Sprintf("add guest SSID %q on %s (internet-only)", p.SSID, p.Radio),
			fmt.Sprintf("create interface/zone/pool %q, forward %s→%s", p.Interface, p.Zone, p.WANZone),
		},
		Apply: func(c model.Config) (model.Config, error) {
			c.Interfaces = appendCopy(c.Interfaces, model.Interface{
				Name: p.Interface, Proto: model.StaticProto{IPAddr: []netip.Prefix{p.Gateway}},
			})
			c.Zones = appendCopy(c.Zones, model.Zone{
				Name: p.Zone, Networks: []string{p.Interface},
				Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT",
			})
			c.Forwardings = appendCopy(c.Forwardings, model.Forwarding{Src: p.Zone, Dest: p.WANZone})
			c.Pools = appendCopy(c.Pools, model.DHCPPool{
				Name: p.Interface, Interface: p.Interface, Start: p.PoolStart, Limit: p.PoolLimit, LeaseTime: p.LeaseTime,
			})
			c.SSIDs = appendCopy(c.SSIDs, model.WifiIface{
				Name: p.Interface, Device: p.Radio, Mode: "ap", SSID: p.SSID,
				Network: p.Interface, Encryption: model.EncPSK2{Key: p.Key}, Isolate: true,
			})
			return c, nil
		},
	}, nil
}

// PortForward exposes an internal host:port on a WAN port (a DNAT redirect).
type PortForward struct {
	Name         string
	Proto        string // tcp / udp; default tcp
	ExternalPort string
	InternalIP   netip.Addr
	InternalPort string
	SrcZone      string // default "wan"
	DestZone     string // default "lan"
}

func ExposeService(_ model.Config, p PortForward) (change.ChangeSet, error) {
	if p.Name == "" || p.ExternalPort == "" || !p.InternalIP.IsValid() || p.InternalPort == "" {
		return change.ChangeSet{}, fmt.Errorf("port forward needs Name, ExternalPort, InternalIP, InternalPort")
	}
	def(&p.Proto, "tcp")
	def(&p.SrcZone, "wan")
	def(&p.DestZone, "lan")
	return change.ChangeSet{
		Summary: []string{fmt.Sprintf("forward %s:%s → %s:%s (%s)", p.SrcZone, p.ExternalPort, p.InternalIP, p.InternalPort, p.Proto)},
		Apply: func(c model.Config) (model.Config, error) {
			c.Redirects = appendCopy(c.Redirects, model.Redirect{
				Name: p.Name, Src: p.SrcZone, SrcDPort: p.ExternalPort,
				Dest: p.DestZone, DestIP: p.InternalIP, DestPort: p.InternalPort,
				Proto: p.Proto, Target: "DNAT",
			})
			return c, nil
		},
	}, nil
}

// Reservation promotes a (typically dynamic) lease to a static one.
type Reservation struct {
	Name     string
	MAC      string
	IP       netip.Addr
	Hostname string
}

func ReserveLease(_ model.Config, r Reservation) (change.ChangeSet, error) {
	if r.MAC == "" || !r.IP.IsValid() {
		return change.ChangeSet{}, fmt.Errorf("reservation needs MAC and IP")
	}
	def(&r.Name, r.Hostname)
	return change.ChangeSet{
		Summary: []string{fmt.Sprintf("reserve %s → %s", r.MAC, r.IP)},
		Apply: func(c model.Config) (model.Config, error) {
			c.Hosts = appendCopy(c.Hosts, model.Host{Name: r.Name, MAC: r.MAC, IP: r.IP, Hostname: r.Hostname})
			return c, nil
		},
	}, nil
}

// WireGuardPeer adds a peer to an existing wireguard interface.
type WireGuardPeer struct {
	Name                string
	Interface           string
	PublicKey           string
	AllowedIPs          []netip.Prefix
	EndpointHost        string
	EndpointPort        int
	PersistentKeepalive int
}

func AddWireGuardPeer(_ model.Config, p WireGuardPeer) (change.ChangeSet, error) {
	if p.Interface == "" || p.PublicKey == "" || len(p.AllowedIPs) == 0 {
		return change.ChangeSet{}, fmt.Errorf("peer needs Interface, PublicKey, and at least one AllowedIP")
	}
	def(&p.Name, p.PublicKey)
	return change.ChangeSet{
		Summary: []string{fmt.Sprintf("add WireGuard peer %q to %s", p.Name, p.Interface)},
		Apply: func(c model.Config) (model.Config, error) {
			c.WGPeers = appendCopy(c.WGPeers, model.WGPeer{
				Name: p.Name, Interface: p.Interface, PublicKey: p.PublicKey,
				AllowedIPs: p.AllowedIPs, EndpointHost: p.EndpointHost,
				EndpointPort: p.EndpointPort, PersistentKeepalive: p.PersistentKeepalive,
			})
			return c, nil
		},
	}, nil
}

// VLAN tags an 802.1q VLAN onto a parent interface: a Device(8021q) plus an
// Interface bound to it. The everyday "add a VLAN" wizard (docs/NET.md §6.2).
type VLAN struct {
	Parent    string            // parent ifname, e.g. eth0
	VID       int               // 1..4094
	Interface string            // friendly interface name; default <parent>_<vid>
	Proto     model.ProtoConfig // addressing; default DHCP
}

func AddVLAN(_ model.Config, p VLAN) (change.ChangeSet, error) {
	if p.Parent == "" || p.VID < 1 || p.VID > 4094 {
		return change.ChangeSet{}, fmt.Errorf("vlan needs a Parent and a VID in 1..4094")
	}
	dev := fmt.Sprintf("%s.%d", p.Parent, p.VID)
	def(&p.Interface, fmt.Sprintf("%s_%d", p.Parent, p.VID))
	if p.Proto == nil {
		p.Proto = model.DHCPProto{}
	}
	return change.ChangeSet{
		Summary: []string{
			fmt.Sprintf("add VLAN %d on %s (device %s, interface %q)", p.VID, p.Parent, dev, p.Interface),
		},
		Apply: func(c model.Config) (model.Config, error) {
			c.Devices = appendCopy(c.Devices, model.Device{Name: dev, Type: "8021q", Ifname: p.Parent, VID: p.VID})
			c.Interfaces = appendCopy(c.Interfaces, model.Interface{Name: p.Interface, Device: dev, Proto: p.Proto})
			return c, nil
		},
	}, nil
}

// Bridge bonds member NICs into a bridge: a Device(bridge) plus an Interface
// that carries the addressing. The members become ports, so any standalone
// interface bound to a member is removed (it's now enslaved). The "bridge these
// interfaces" wizard (docs/NET.md §6.2).
type Bridge struct {
	Name      string            // bridge ifname, e.g. br0
	Interface string            // friendly interface name; default = Name
	Members   []string          // member ifnames, e.g. eth1, eth2
	Proto     model.ProtoConfig // addressing on the bridge; default DHCP
}

func AddBridge(_ model.Config, p Bridge) (change.ChangeSet, error) {
	if p.Name == "" || len(p.Members) == 0 {
		return change.ChangeSet{}, fmt.Errorf("bridge needs a Name and at least one Member")
	}
	def(&p.Interface, p.Name)
	if p.Proto == nil {
		p.Proto = model.DHCPProto{}
	}
	isMember := map[string]bool{}
	for _, m := range p.Members {
		isMember[m] = true
	}
	return change.ChangeSet{
		Summary: []string{
			fmt.Sprintf("bridge %s over %v", p.Name, p.Members),
			fmt.Sprintf("create interface %q on %s", p.Interface, p.Name),
		},
		Apply: func(c model.Config) (model.Config, error) {
			// Members become ports — drop any standalone interface bound to one.
			var kept []model.Interface
			for _, i := range c.Interfaces {
				if !isMember[i.Device] {
					kept = append(kept, i)
				}
			}
			c.Interfaces = kept
			c.Devices = appendCopy(c.Devices, model.Device{Name: p.Name, Type: "bridge", Ports: p.Members})
			c.Interfaces = appendCopy(c.Interfaces, model.Interface{Name: p.Interface, Device: p.Name, Proto: p.Proto})
			return c, nil
		},
	}, nil
}

func def(s *string, fallback string) {
	if *s == "" {
		*s = fallback
	}
}

// appendCopy appends to a copy of s, never aliasing or mutating the input.
func appendCopy[T any](s []T, v ...T) []T {
	out := make([]T, len(s), len(s)+len(v))
	copy(out, s)
	return append(out, v...)
}
