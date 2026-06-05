package codec

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// Stock OpenWRT compatibility: a real box ships split IPv4 (ipaddr + netmask)
// and the ip6assign prefix-delegation pattern — neither of which the model spoke
// natively. These lock down reading such a box.

func TestParseStockOpenWRTAddressing(t *testing.T) {
	// A stock dual-stack OpenWRT lan: split v4, ip6assign, static ip6addr.
	netCfg := `
config interface 'lan'
	option device 'br-lan'
	option proto 'static'
	option ipaddr '192.168.1.1'
	option netmask '255.255.255.0'
	option ip6assign '60'
	option ip6addr 'fd00::1/64'
`
	c, err := Parse(map[string]string{"network": netCfg})
	if err != nil {
		t.Fatalf("Parse stock OpenWRT: %v", err)
	}
	if len(c.Interfaces) != 1 {
		t.Fatalf("expected 1 interface, got %+v", c.Interfaces)
	}
	lan := c.Interfaces[0]
	if lan.IP6Assign != 60 {
		t.Errorf("ip6assign: got %d want 60", lan.IP6Assign)
	}
	sp, ok := lan.Proto.(model.StaticProto)
	if !ok {
		t.Fatalf("proto: got %T want StaticProto", lan.Proto)
	}
	if sp.IPAddr.String() != "192.168.1.1/24" {
		t.Errorf("split v4 ipaddr+netmask not folded to CIDR: got %q want 192.168.1.1/24", sp.IPAddr)
	}
	if sp.IP6Addr.String() != "fd00::1/64" {
		t.Errorf("static ip6addr: got %q want fd00::1/64", sp.IP6Addr)
	}
}

func TestNetmaskBits(t *testing.T) {
	for mask, want := range map[string]int{
		"255.255.255.0": 24, "255.255.0.0": 16, "255.0.0.0": 8,
		"255.255.255.252": 30, "0.0.0.0": 0, "255.255.255.255": 32,
	} {
		if n, ok := netmaskBits(mask); !ok || n != want {
			t.Errorf("netmaskBits(%q) = %d,%v want %d,true", mask, n, ok, want)
		}
	}
	if _, ok := netmaskBits("255.0.255.0"); ok {
		t.Error("non-contiguous mask 255.0.255.0 should be rejected")
	}
	if _, ok := netmaskBits("not-a-mask"); ok {
		t.Error("garbage netmask should be rejected")
	}
}

func TestCIDRIpaddrUntouched(t *testing.T) {
	// An already-CIDR ipaddr (wash's own output) must pass through unchanged.
	c, err := Parse(map[string]string{"network": "config interface 'lan'\n\toption proto 'static'\n\toption ipaddr '10.0.0.1/24'\n"})
	if err != nil {
		t.Fatal(err)
	}
	if sp := c.Interfaces[0].Proto.(model.StaticProto); sp.IPAddr.String() != "10.0.0.1/24" {
		t.Errorf("CIDR ipaddr mangled: %q", sp.IPAddr)
	}
}

func TestIP6AssignRoundTrips(t *testing.T) {
	c := model.Config{Interfaces: []model.Interface{
		{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("10.0.0.1/24")}, IP6Assign: 60},
	}}
	files, err := Render(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(files["network"], "ip6assign '60'") {
		t.Errorf("ip6assign not rendered:\n%s", files["network"])
	}
	back, err := Parse(files)
	if err != nil {
		t.Fatal(err)
	}
	if back.Interfaces[0].IP6Assign != 60 {
		t.Errorf("ip6assign did not round-trip: got %d", back.Interfaces[0].IP6Assign)
	}
}
