package change

import (
	"net/netip"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

func has(d Diff, kind, name string, op Op) bool {
	for _, e := range d.Entries {
		if e.Kind == kind && e.Name == name && e.Op == op {
			return true
		}
	}
	return false
}

func TestDiffAddRemoveUpdate(t *testing.T) {
	base := model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")}}},
			{Name: "wan", Proto: model.DHCPProto{}},
		},
	}
	after := model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("10.0.0.2/24")}}}, // changed
			{Name: "guest", Proto: model.DHCPProto{}},                                                             // added
			// wan removed
		},
	}
	d := Compute(base, after)
	if !has(d, "network/interface", "lan", OpUpdate) {
		t.Error("want update lan")
	}
	if !has(d, "network/interface", "guest", OpAdd) {
		t.Error("want add guest")
	}
	if !has(d, "network/interface", "wan", OpRemove) {
		t.Error("want remove wan")
	}
}

func TestDiffEmptyWhenEqual(t *testing.T) {
	c := model.Config{
		Zones:       []model.Zone{{Name: "lan", Input: "ACCEPT"}},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
	}
	if d := Compute(c, c); !d.Empty() {
		t.Fatalf("equal configs should diff empty, got %+v", d.Entries)
	}
}

func TestDiffAnonymousByContent(t *testing.T) {
	base := model.Config{Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}}}
	after := model.Config{Forwardings: []model.Forwarding{{Src: "guest", Dest: "wan"}}}
	d := Compute(base, after)
	// Anonymous sections diff by value: one add, one remove.
	var adds, removes int
	for _, e := range d.Entries {
		if e.Kind == "firewall/forwarding" {
			switch e.Op {
			case OpAdd:
				adds++
			case OpRemove:
				removes++
			}
		}
	}
	if adds != 1 || removes != 1 {
		t.Fatalf("want 1 add + 1 remove for anonymous change, got %+v", d.Entries)
	}
}
