package fabric

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// stable asserts the lens round-trips: Materialize(Project(Materialize(p))) ==
// Materialize(p). Comparing materialized output (canonical) sidesteps any
// ordering noise in a hand-written Plan.
func stable(t *testing.T, name string, p Plan) {
	t.Helper()
	d1, b1 := Materialize(p)
	pp, leftovers := Project(d1, b1)
	if len(leftovers) != 0 {
		t.Fatalf("%s: unexpected leftovers: %#v", name, leftovers)
	}
	d2, b2 := Materialize(pp)
	if !reflect.DeepEqual(d1, d2) || !reflect.DeepEqual(b1, b2) {
		t.Fatalf("%s: not stable\n devices1=%#v\n devices2=%#v\n bvlans1=%#v\n bvlans2=%#v", name, d1, d2, b1, b2)
	}
}

func TestLensStable(t *testing.T) {
	cases := map[string]Plan{
		"bare native port": {
			VLANs: []VLAN{{ID: 1, Routed: true}},
			Ports: []Port{{Name: "eth0", Untagged: 1}},
		},
		"plain bridge (2 native ports, no filtering)": {
			VLANs: []VLAN{{ID: 1, Routed: true}},
			Ports: []Port{{Name: "eth1", Untagged: 1}, {Name: "eth2", Untagged: 1}},
		},
		"lone tagged uplink → sub-interface": {
			VLANs: []VLAN{{ID: 100, Routed: true}},
			Ports: []Port{{Name: "eth0", Tagged: []int{100}}},
		},
		"vlan-filtering: access ports + trunk + transit": {
			VLANs: []VLAN{{ID: 1, Routed: true}, {ID: 20, Routed: true}, {ID: 30, Routed: false}},
			Ports: []Port{
				{Name: "eth1", Untagged: 1},
				{Name: "eth2", Untagged: 20},
				{Name: "eth3", Untagged: 30},
				{Name: "eth4", Untagged: 1, Tagged: []int{20, 30}},
			},
		},
		"mixed: switch fabric + a separate tagged WAN uplink": {
			VLANs: []VLAN{{ID: 1, Routed: true}, {ID: 20, Routed: true}, {ID: 100, Routed: true}},
			Ports: []Port{
				{Name: "lan1", Untagged: 1},
				{Name: "lan2", Untagged: 20},
				{Name: "wan0", Tagged: []int{100}}, // lone → eth.100 sub-iface
			},
		},
	}
	for name, p := range cases {
		t.Run(name, func(t *testing.T) { stable(t, name, p) })
	}
}

// TestLensStableProperty fuzzes valid plans (each port has ≤1 PVID + a tagged
// subset, no port untagged in two VLANs) and asserts the lens is stable.
func TestLensStableProperty(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	vids := []int{1, 10, 20, 30} // 1 = native
	for i := 0; i < 500; i++ {
		nports := 1 + r.Intn(5)
		var p Plan
		seen := map[int]bool{}
		for j := 0; j < nports; j++ {
			pt := Port{Name: fmt.Sprintf("eth%d", j)}
			if r.Intn(2) == 0 {
				pt.Untagged = vids[r.Intn(len(vids))]
				seen[pt.Untagged] = true
			}
			for _, v := range vids {
				if v != pt.Untagged && r.Intn(3) == 0 {
					pt.Tagged = append(pt.Tagged, v)
					seen[v] = true
				}
			}
			p.Ports = append(p.Ports, pt)
		}
		for v := range seen {
			p.VLANs = append(p.VLANs, VLAN{ID: v, Routed: r.Intn(4) != 0})
		}
		stable(t, fmt.Sprintf("fuzz-%d", i), p)
	}
}

func hasVLAN(b []model.BridgeVLAN, vid int) bool {
	for _, x := range b {
		if x.VLAN == vid {
			return true
		}
	}
	return false
}

func TestMaterializeShapes(t *testing.T) {
	// plain bridge: no filtering, no bridge-vlan.
	d, b := Materialize(Plan{Ports: []Port{{Name: "eth1", Untagged: 1}, {Name: "eth2", Untagged: 1}}})
	if len(d) != 1 || d[0].Name != "br-lan" || d[0].VLANFiltering || len(b) != 0 {
		t.Fatalf("plain bridge wrong: %#v / %#v", d, b)
	}
	// a tagged trunk port stays a br-lan member (no sub-interface), filtering on.
	d, b = Materialize(Plan{VLANs: []VLAN{{ID: 100, Routed: true}}, Ports: []Port{{Name: "eth0", Tagged: []int{100}}}})
	if len(d) != 1 || d[0].Name != BridgeName || !d[0].VLANFiltering {
		t.Fatalf("tagged trunk should be a filtering br-lan member: %#v", d)
	}
	if !hasVLAN(b, 100) {
		t.Fatalf("expected a bridge-vlan 100: %#v", b)
	}
	// transit VLAN renders local '0'.
	_, b = Materialize(Plan{
		VLANs: []VLAN{{ID: 1, Routed: true}, {ID: 30, Routed: false}},
		Ports: []Port{{Name: "eth1", Untagged: 1}, {Name: "eth2", Untagged: 30}},
	})
	var v30 *model.BridgeVLAN
	for i := range b {
		if b[i].VLAN == 30 {
			v30 = &b[i]
		}
	}
	if v30 == nil || v30.Local != "0" {
		t.Fatalf("transit VLAN should set local '0': %#v", b)
	}
}
