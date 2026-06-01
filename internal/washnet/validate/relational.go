package validate

import (
	"fmt"
	"net/netip"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// relational runs the cross-object invariants that UCI/LuCI do not check and
// where real misconfiguration lives (docs/NET.md §4.2): name uniqueness,
// reference existence, subnet/AllowedIPs/bridge-port overlap, static-lease
// uniqueness, and firewall-rule shadowing.
func (d *diags) relational(c model.Config) {
	// --- name sets (+ uniqueness) ---
	ifaceSet := map[string]bool{}
	ifaceProto := map[string]model.ProtoConfig{}
	for i, o := range c.Interfaces {
		if o.Name == "" {
			continue
		}
		if ifaceSet[o.Name] {
			d.add(at("Interfaces", i)+".Name", "duplicate_name", dup("interface", o.Name), Error)
		}
		ifaceSet[o.Name] = true
		ifaceProto[o.Name] = o.Proto
	}
	zoneSet := uniqNames(d, c.Zones, "Zones", "zone", func(z model.Zone) string { return z.Name })
	radioSet := uniqNames(d, c.Radios, "Radios", "radio", func(r model.WifiDevice) string { return r.Name })
	uniqNames(d, c.Devices, "Devices", "device", func(dev model.Device) string { return dev.Name })

	// --- reference existence ---
	refZone := func(path, field, val string) {
		if val != "" && val != "*" && !zoneSet[val] {
			d.add(path+"."+field, "unknown_zone", fmt.Sprintf("%s references unknown zone %q", field, val), Error)
		}
	}
	for i, o := range c.Forwardings {
		refZone(at("Forwardings", i), "Src", o.Src)
		refZone(at("Forwardings", i), "Dest", o.Dest)
	}
	for i, o := range c.Redirects {
		refZone(at("Redirects", i), "Src", o.Src)
		refZone(at("Redirects", i), "Dest", o.Dest)
	}
	for i, o := range c.FwRules {
		refZone(at("FwRules", i), "Src", o.Src)
		refZone(at("FwRules", i), "Dest", o.Dest)
	}
	for i, o := range c.Zones {
		for _, n := range o.Networks {
			if n != "" && !ifaceSet[n] {
				d.add(at("Zones", i)+".Networks", "unknown_network", fmt.Sprintf("references unknown network %q", n), Error)
			}
		}
	}
	for i, o := range c.SSIDs {
		if o.Device != "" && !radioSet[o.Device] {
			d.add(at("SSIDs", i)+".Device", "unknown_radio", fmt.Sprintf("references unknown radio %q", o.Device), Error)
		}
		if o.Network != "" && !ifaceSet[o.Network] {
			d.add(at("SSIDs", i)+".Network", "unknown_network", fmt.Sprintf("references unknown network %q", o.Network), Error)
		}
	}
	for i, o := range c.WGPeers {
		if o.Interface == "" {
			continue
		}
		if !ifaceSet[o.Interface] {
			d.add(at("WGPeers", i)+".Interface", "unknown_network", fmt.Sprintf("references unknown interface %q", o.Interface), Error)
		} else if _, ok := ifaceProto[o.Interface].(model.WireGuardProto); !ok {
			d.add(at("WGPeers", i)+".Interface", "not_wireguard", fmt.Sprintf("interface %q is not a wireguard interface", o.Interface), Error)
		}
	}
	for i, o := range c.Routes {
		if o.Interface != "" && !ifaceSet[o.Interface] {
			d.add(at("Routes", i)+".Interface", "unknown_network", fmt.Sprintf("references unknown interface %q", o.Interface), Error)
		}
	}
	for i, o := range c.Pools {
		ref := o.Interface
		if ref == "" {
			ref = o.Name // OpenWRT: the pool section name is the interface
		}
		if ref != "" && !ifaceSet[ref] {
			d.add(at("Pools", i)+".Interface", "unknown_network", fmt.Sprintf("DHCP pool references unknown interface %q", ref), Error)
		}
	}

	// --- overlapping static subnets ---
	type pfx struct {
		i int
		p netip.Prefix
	}
	var statics []pfx
	for i, o := range c.Interfaces {
		if sp, ok := o.Proto.(model.StaticProto); ok && sp.IPAddr.IsValid() {
			statics = append(statics, pfx{i, sp.IPAddr})
		}
	}
	for a := 0; a < len(statics); a++ {
		for b := a + 1; b < len(statics); b++ {
			if statics[a].p.Overlaps(statics[b].p) {
				d.add(at("Interfaces", statics[b].i)+".IPAddr", "subnet_overlap",
					fmt.Sprintf("subnet %s overlaps Interfaces[%d] (%s)", statics[b].p, statics[a].i, statics[a].p), Error)
			}
		}
	}

	// --- WireGuard AllowedIPs overlap per interface ---
	byIface := map[string][]pfx{}
	for i, o := range c.WGPeers {
		for _, p := range o.AllowedIPs {
			byIface[o.Interface] = append(byIface[o.Interface], pfx{i, p})
		}
	}
	for _, ps := range byIface {
		for a := 0; a < len(ps); a++ {
			for b := a + 1; b < len(ps); b++ {
				if ps[a].p.Overlaps(ps[b].p) {
					d.add(at("WGPeers", ps[b].i)+".AllowedIPs", "allowedips_overlap",
						fmt.Sprintf("AllowedIPs %s overlaps WGPeers[%d] (%s) on the same interface", ps[b].p, ps[a].i, ps[a].p), Error)
				}
			}
		}
	}

	// --- bridge ports disjoint ---
	portOwner := map[string]int{}
	for i, o := range c.Devices {
		if o.Type != "bridge" {
			continue
		}
		for _, port := range o.Ports {
			if prev, ok := portOwner[port]; ok {
				d.add(at("Devices", i)+".Ports", "port_conflict", fmt.Sprintf("port %q already used by Devices[%d]", port, prev), Error)
			} else {
				portOwner[port] = i
			}
		}
	}

	// --- static-lease uniqueness ---
	macOwner := map[string]int{}
	ipOwner := map[string]int{}
	for i, o := range c.Hosts {
		if o.MAC != "" {
			mac := strings.ToLower(o.MAC)
			if prev, ok := macOwner[mac]; ok {
				d.add(at("Hosts", i)+".MAC", "duplicate_mac", fmt.Sprintf("MAC already used by Hosts[%d]", prev), Error)
			} else {
				macOwner[mac] = i
			}
		}
		if o.IP.IsValid() {
			if prev, ok := ipOwner[o.IP.String()]; ok {
				d.add(at("Hosts", i)+".IP", "duplicate_ip", fmt.Sprintf("IP already used by Hosts[%d]", prev), Error)
			} else {
				ipOwner[o.IP.String()] = i
			}
		}
	}

	// --- firewall-rule shadowing (conservative: exact match behind a terminal rule) ---
	type matchKey struct{ src, dest, proto, sport, dport string }
	terminal := map[matchKey]int{}
	isTerminal := func(t string) bool { return t == "ACCEPT" || t == "REJECT" || t == "DROP" }
	for i, o := range c.FwRules {
		k := matchKey{o.Src, o.Dest, o.Proto, o.SrcPort, o.DestPort}
		if prev, ok := terminal[k]; ok {
			d.add(at("FwRules", i), "shadowed_rule", fmt.Sprintf("rule is unreachable; FwRules[%d] already matches and terminates", prev), Warning)
		}
		if isTerminal(o.Target) {
			if _, ok := terminal[k]; !ok {
				terminal[k] = i
			}
		}
	}
}

func uniqNames[T any](d *diags, slice []T, field, kind string, name func(T) string) map[string]bool {
	set := map[string]bool{}
	for i, o := range slice {
		n := name(o)
		if n == "" {
			continue
		}
		if set[n] {
			d.add(at(field, i)+".Name", "duplicate_name", dup(kind, n), Error)
		}
		set[n] = true
	}
	return set
}

func dup(kind, name string) string { return fmt.Sprintf("%s name %q is already used", kind, name) }
