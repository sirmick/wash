// Package fabric is the L2-fabric lens (NET-ROUTER-UI.md §4c): a pure,
// bidirectional mapping between a box-global port×VLAN Plan — the unified table
// the UI edits — and the OpenWRT device layer that realises it (bridges,
// bridge-vlan sections, 8021q sub-interfaces). It picks the idiomatic UCI shape
// by topology so "bridge vs sub-interface" is never a user concept:
//
//   - lone native-untagged port, unshared          → bare port (no device)
//   - lone tagged uplink, unshared                  → eth0.<vid> sub-interface(s)
//   - ≥2 ports sharing only the native domain       → a plain br-lan bridge
//   - any tagging / multiple VLANs on shared ports  → one vlan_filtering br-lan
//                                                      + a bridge-vlan per VLAN
//
// Scope: one switch bridge (auto-named br-lan, home-router decision). Devices the
// lens doesn't model (macvlan, QinQ, bonds, extra bridges) are returned as
// leftovers and preserved verbatim. The governing law is that the lens is stable
// through a round-trip: Materialize(Project(Materialize(p))) == Materialize(p).
package fabric

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sirmick/wash/internal/washnet/model"
)

// NativeVID is the untagged/native domain (OpenWRT's default PVID). Ports that
// share it untagged form the plain LAN; it only becomes a real VLAN 1 once
// filtering is forced by another VLAN on the same bridge.
const NativeVID = 1

// BridgeName is the single switch bridge the lens materialises to.
const BridgeName = "br-lan"

// VLAN is one box-global VLAN. Routed=false ⇒ transit (bridge-vlan local '0').
type VLAN struct {
	ID     int
	Routed bool
}

// Port is one physical port's membership: at most one Untagged (its PVID; 0 =
// none) and any number of Tagged VLANs.
type Port struct {
	Name     string
	Untagged int
	Tagged   []int
}

// Plan is the unified table.
type Plan struct {
	VLANs []VLAN
	Ports []Port
}

func (p Port) tagged(v int) bool {
	for _, t := range p.Tagged {
		if t == v {
			return true
		}
	}
	return false
}

// Materialize turns a Plan into the L2 device layer (devices + bridge-vlans).
func Materialize(p Plan) ([]model.Device, []model.BridgeVLAN) {
	routed := map[int]bool{NativeVID: true}
	for _, v := range p.VLANs {
		routed[v.ID] = v.Routed
	}
	// member ports per VLAN (untagged or tagged), to know what's shared.
	members := map[int][]string{}
	for _, pt := range p.Ports {
		if pt.Untagged != 0 {
			members[pt.Untagged] = append(members[pt.Untagged], pt.Name)
		}
		for _, v := range pt.Tagged {
			members[v] = append(members[v], pt.Name)
		}
	}
	shared := func(pt Port) bool {
		if pt.Untagged != 0 && len(members[pt.Untagged]) > 1 {
			return true
		}
		for _, v := range pt.Tagged {
			if len(members[v]) > 1 {
				return true
			}
		}
		return false
	}

	var devices []model.Device
	var subs []model.Device
	var brPorts []string
	for _, pt := range p.Ports {
		hasTag := len(pt.Tagged) > 0
		hasUntag := pt.Untagged != 0
		switch {
		case !hasTag && !hasUntag:
			// not in any VLAN — a free port, no device.
		case hasTag && !hasUntag && !shared(pt):
			// lone tagged uplink → sub-interface(s).
			tags := append([]int(nil), pt.Tagged...)
			sort.Ints(tags)
			for _, v := range tags {
				subs = append(subs, model.Device{Name: fmt.Sprintf("%s.%d", pt.Name, v), Type: "8021q", Ifname: pt.Name, VID: v})
			}
		case hasUntag && !hasTag && pt.Untagged == NativeVID && !shared(pt):
			// lone native-untagged port → bare, no device.
		default:
			brPorts = append(brPorts, pt.Name)
		}
	}

	if len(brPorts) > 0 {
		sort.Strings(brPorts)
		dev := model.Device{Name: BridgeName, Type: "bridge", Ports: brPorts}
		if bridgeNeedsFiltering(p, brPorts) {
			dev.VLANFiltering = true
		}
		devices = append(devices, dev)
	}
	bvlans := materializeBridgeVlans(p, brPorts, routed)
	devices = append(devices, subs...)
	return devices, bvlans
}

// bridgeNeedsFiltering: a bridge is VLAN-aware unless its ports share only the
// plain native domain (no tags, no numbered VLAN).
func bridgeNeedsFiltering(p Plan, brPorts []string) bool {
	in := map[string]bool{}
	for _, n := range brPorts {
		in[n] = true
	}
	vids := map[int]bool{}
	anyTag := false
	for _, pt := range p.Ports {
		if !in[pt.Name] {
			continue
		}
		if pt.Untagged != 0 {
			vids[pt.Untagged] = true
		}
		for _, v := range pt.Tagged {
			vids[v] = true
			anyTag = true
		}
	}
	return anyTag || len(vids) > 1 || (len(vids) == 1 && !vids[NativeVID])
}

func materializeBridgeVlans(p Plan, brPorts []string, routed map[int]bool) []model.BridgeVLAN {
	if len(brPorts) == 0 || !bridgeNeedsFiltering(p, brPorts) {
		return nil // no bridge, or a plain (unfiltered) bridge → no bridge-vlan sections
	}
	in := map[string]bool{}
	for _, n := range brPorts {
		in[n] = true
	}
	vids := map[int]bool{}
	for _, pt := range p.Ports {
		if !in[pt.Name] {
			continue
		}
		if pt.Untagged != 0 {
			vids[pt.Untagged] = true
		}
		for _, v := range pt.Tagged {
			vids[v] = true
		}
	}
	ids := make([]int, 0, len(vids))
	for v := range vids {
		ids = append(ids, v)
	}
	sort.Ints(ids)
	var out []model.BridgeVLAN
	for _, v := range ids {
		var ports []string
		for _, name := range brPorts {
			pt := findPort(p.Ports, name)
			if pt.Untagged == v {
				ports = append(ports, name+":u*")
			} else if pt.tagged(v) {
				ports = append(ports, name+":t")
			}
		}
		bv := model.BridgeVLAN{Device: BridgeName, VLAN: v, Ports: ports}
		if !routed[v] {
			bv.Local = "0"
		}
		out = append(out, bv)
	}
	return out
}

func findPort(ports []Port, name string) Port {
	for _, p := range ports {
		if p.Name == name {
			return p
		}
	}
	return Port{Name: name}
}

// Project reads the L2 device layer back into a Plan. Devices the lens doesn't
// model are returned as leftovers (preserved verbatim).
func Project(devices []model.Device, bvlans []model.BridgeVLAN) (Plan, []model.Device) {
	ports := map[string]*Port{}
	port := func(name string) *Port {
		if ports[name] == nil {
			ports[name] = &Port{Name: name}
		}
		return ports[name]
	}
	vlans := map[int]bool{} // id -> routed
	setVlan := func(id int, routed bool) {
		if was, ok := vlans[id]; !ok || (was && !routed) {
			vlans[id] = routed
		}
	}

	var leftovers []model.Device
	var br *model.Device
	for i := range devices {
		d := devices[i]
		switch {
		case d.Type == "bridge" && d.Name == BridgeName:
			b := d
			br = &b
		case d.Type == "8021q":
			// eth0.10 → port eth0 tagged in 10.
			pt := port(d.Ifname)
			if !pt.tagged(d.VID) {
				pt.Tagged = append(pt.Tagged, d.VID)
			}
			setVlan(d.VID, true)
		default:
			leftovers = append(leftovers, d)
		}
	}

	if br != nil {
		if !br.VLANFiltering {
			// plain bridge: all members native-untagged.
			for _, n := range br.Ports {
				port(n).Untagged = NativeVID
			}
			setVlan(NativeVID, true)
		} else {
			for _, bv := range bvlans {
				if bv.Device != BridgeName {
					continue
				}
				routedV := bv.Local != "0"
				setVlan(bv.VLAN, routedV)
				for _, entry := range bv.Ports {
					name, tagged, pvid := parseEntry(entry)
					pt := port(name)
					if tagged {
						if !pt.tagged(bv.VLAN) {
							pt.Tagged = append(pt.Tagged, bv.VLAN)
						}
					} else if pvid {
						pt.Untagged = bv.VLAN
					}
				}
			}
		}
	}

	plan := Plan{}
	names := make([]string, 0, len(ports))
	for n := range ports {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		pt := ports[n]
		sort.Ints(pt.Tagged)
		plan.Ports = append(plan.Ports, *pt)
	}
	ids := make([]int, 0, len(vlans))
	for id := range vlans {
		ids = append(ids, id)
	}
	sort.Ints(ids)
	for _, id := range ids {
		plan.VLANs = append(plan.VLANs, VLAN{ID: id, Routed: vlans[id]})
	}
	return plan, leftovers
}

// parseEntry decodes a bridge-vlan Ports entry: "<port>"|"<port>:t" (tagged),
// "<port>:u"|"<port>:u*" (untagged, * = PVID).
func parseEntry(e string) (name string, tagged, pvid bool) {
	i := strings.IndexByte(e, ':')
	if i < 0 {
		return e, true, false
	}
	name, suf := e[:i], e[i+1:]
	if strings.HasPrefix(suf, "u") {
		return name, false, strings.Contains(suf, "*")
	}
	return name, true, false
}
