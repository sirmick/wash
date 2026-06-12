// Package segment is a pure, bidirectional lens over model.Config that re-shapes
// the normalized model into the "segment" abstraction the router UI thinks in — a
// segment = an L3 interface + its carrier (VLAN/bridge) + firewall zone + DHCP
// pool as one node — plus the zone×zone firewall policy as the edges. It is NOT a
// new source of truth and NOT a CLI: the model + the UCI codec/apply path stay
// authoritative; this only groups and ungroups (docs/NET-ROUTER-UI.md §8b).
//
// The governing law is the round-trip Materialize(Project(c)) == c (as a multiset
// per kind — see canon in the tests): Project partitions every object into exactly
// one of {a segment, the policy, leftovers}, and Materialize reassembles them.
// Leftovers is a full model.Config carrying every kind the lens doesn't absorb
// verbatim, so nothing is ever dropped and the round-trip survives model growth.
package segment

import "github.com/sirmick/wash/internal/washnet/model"

// Segment is one network segment: the L3 interface and the objects it owns. The
// objects are held directly (not summarized) so the round-trip is exact; the
// UI-facing shape (Role, Carrier, addresses) is computed from them on demand, so
// there's no derived state to drift or to break the round-trip.
type Segment struct {
	Name   string // == Iface.Name
	Iface  model.Interface
	Device *model.Device   // the VLAN/bridge device backing the interface, if any
	Zone   *model.Zone     // the single-network firewall zone for this interface, if any
	Pool   *model.DHCPPool // the DHCP pool serving this interface, if any
}

// Policy is the firewall edges — the zone×zone matrix — kept separate from the
// segment nodes (the relations are N×N and don't belong inside a node). Order is
// preserved for the rule list (fw4 evaluates rules in order).
type Policy struct {
	Defaults    []model.Defaults
	Forwardings []model.Forwarding
	Rules       []model.FirewallRule
}

// Projection is the full lens output: the segment nodes, the firewall policy, and
// everything else (routes, redirects, NAT, hosts, DNS, wifi, unmatched
// devices/zones/pools, …) carried verbatim as Leftovers — the Advanced raw view.
type Projection struct {
	Segments  []Segment
	Policy    Policy
	Leftovers model.Config
}

// Role is the segment's purpose, derived from its objects.
type Role string

const (
	RoleLAN Role = "lan"
	RoleWAN Role = "wan"
	RoleVPN Role = "vpn"
)

// Role derives the segment's purpose: a WireGuard interface is a VPN egress; a
// masquerading zone marks the WAN uplink; everything else is a LAN segment.
func (s Segment) Role() Role {
	if _, ok := s.Iface.Proto.(model.WireGuardProto); ok {
		return RoleVPN
	}
	if s.Zone != nil && s.Zone.Masq {
		return RoleWAN
	}
	return RoleLAN
}

// CarrierKind is how the segment attaches to L1/L2.
type CarrierKind string

const (
	CarrierUntagged CarrierKind = "untagged" // a raw port / the interface's own device
	CarrierVLAN     CarrierKind = "vlan"     // an 802.1q tag on a trunk
	CarrierBridge   CarrierKind = "bridge"   // a bridge of member ports
)

// Carrier is the segment's L1/L2 attachment, derived from its backing device.
type Carrier struct {
	Kind    CarrierKind
	Port    string   // untagged: the device; vlan: the trunk (Device.Ifname)
	VID     int      // vlan only
	Members []string // bridge only
}

// Carrier inspects the segment's backing device to classify its attachment.
func (s Segment) Carrier() Carrier {
	if s.Device != nil {
		switch s.Device.Type {
		case "8021q":
			return Carrier{Kind: CarrierVLAN, Port: s.Device.Ifname, VID: s.Device.VID}
		case "bridge":
			return Carrier{Kind: CarrierBridge, Members: append([]string(nil), s.Device.Ports...)}
		}
	}
	return Carrier{Kind: CarrierUntagged, Port: s.Iface.Device}
}

// StaticAddrs returns the segment's configured static addresses, if it's static.
func (s Segment) StaticAddrs() []string {
	if sp, ok := s.Iface.Proto.(model.StaticProto); ok {
		out := make([]string, 0, len(sp.IPAddr))
		for _, a := range sp.IPAddr {
			out = append(out, a.String())
		}
		return out
	}
	return nil
}

// Project groups a model.Config into segments + policy + leftovers. Every
// Interface becomes a segment (loopback included — degenerate, the UI filters it);
// a Device is absorbed only when exactly one interface references it; a Zone is
// absorbed when it has exactly one network naming this interface; a DHCPPool when
// its interface names it. All firewall defaults/forwardings/rules become Policy.
// Everything not absorbed stays in Leftovers verbatim.
func Project(c model.Config) Projection {
	usedDev := make([]bool, len(c.Devices))
	usedZone := make([]bool, len(c.Zones))
	usedPool := make([]bool, len(c.Pools))

	// A device is owned by a segment only if exactly one interface references it
	// (a shared device would otherwise be duplicated on materialize).
	devRefs := map[string]int{}
	for _, i := range c.Interfaces {
		if i.Device != "" {
			devRefs[i.Device]++
		}
	}

	var segs []Segment
	for _, i := range c.Interfaces {
		s := Segment{Name: i.Name, Iface: i}
		if i.Device != "" && devRefs[i.Device] == 1 {
			for j := range c.Devices {
				if !usedDev[j] && c.Devices[j].Name == i.Device {
					d := c.Devices[j]
					s.Device = &d
					usedDev[j] = true
					break
				}
			}
		}
		for j := range c.Zones {
			z := c.Zones[j]
			if !usedZone[j] && len(z.Networks) == 1 && z.Networks[0] == i.Name {
				s.Zone = &z
				usedZone[j] = true
				break
			}
		}
		for j := range c.Pools {
			if !usedPool[j] && c.Pools[j].Interface == i.Name {
				p := c.Pools[j]
				s.Pool = &p
				usedPool[j] = true
				break
			}
		}
		segs = append(segs, s)
	}

	left := c // struct copy; we replace the absorbed slices, leave the rest aliased
	left.Interfaces = nil
	left.FwDefaults = nil
	left.Forwardings = nil
	left.FwRules = nil
	left.Devices = keepUnused(c.Devices, usedDev)
	left.Zones = keepUnused(c.Zones, usedZone)
	left.Pools = keepUnused(c.Pools, usedPool)

	return Projection{
		Segments:  segs,
		Policy:    Policy{Defaults: c.FwDefaults, Forwardings: c.Forwardings, Rules: c.FwRules},
		Leftovers: left,
	}
}

// Materialize reassembles a Projection back into a model.Config. It starts from
// Leftovers (which carries every un-absorbed kind verbatim) and re-emits the
// segment-owned objects and the policy, producing nil for empty kinds so a
// freshly-built config compares equal to a parsed one.
func Materialize(p Projection) model.Config {
	c := p.Leftovers // untouched kinds (routes, hosts, dns, wifi, …) flow through

	// Start from any leftover interfaces (Project nils these, so the round-trip is
	// unaffected; a hand-built Projection may carry e.g. loopback here), then add
	// the segment interfaces.
	ifaces := append([]model.Interface(nil), p.Leftovers.Interfaces...)
	devices := append([]model.Device(nil), p.Leftovers.Devices...)
	zones := append([]model.Zone(nil), p.Leftovers.Zones...)
	pools := append([]model.DHCPPool(nil), p.Leftovers.Pools...)
	for _, s := range p.Segments {
		ifaces = append(ifaces, s.Iface)
		if s.Device != nil {
			devices = append(devices, *s.Device)
		}
		if s.Zone != nil {
			zones = append(zones, *s.Zone)
		}
		if s.Pool != nil {
			pools = append(pools, *s.Pool)
		}
	}
	c.Interfaces = ifaces
	c.Devices = devices
	c.Zones = zones
	c.Pools = pools
	c.FwDefaults = append([]model.Defaults(nil), p.Policy.Defaults...)
	c.Forwardings = append([]model.Forwarding(nil), p.Policy.Forwardings...)
	c.FwRules = append([]model.FirewallRule(nil), p.Policy.Rules...)
	return c
}

// keepUnused returns the elements of xs whose used flag is false (a new slice;
// nil when none remain, so empties stay nil for round-trip equality).
func keepUnused[T any](xs []T, used []bool) []T {
	var out []T
	for i := range xs {
		if !used[i] {
			out = append(out, xs[i])
		}
	}
	return out
}
