package validate

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

// findCode returns the first diagnostic with the given code whose path contains
// pathSub, or nil.
func find(ds []Diagnostic, code, pathSub string) *Diagnostic {
	for i := range ds {
		if ds[i].Code == code && strings.Contains(ds[i].Path, pathSub) {
			return &ds[i]
		}
	}
	return nil
}

func errorCount(ds []Diagnostic) int {
	n := 0
	for _, d := range ds {
		if d.Severity == Error {
			n++
		}
	}
	return n
}

// TestValidValid: a well-formed gateway under Full caps yields zero errors.
func TestValidConfigUnderFull(t *testing.T) {
	c := model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("192.168.1.1/24")}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
		},
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
		},
		Redirects: []model.Redirect{{Name: "ssh", Src: "wan", SrcDPort: "2222", Dest: "lan", DestPort: "22", Target: "DNAT"}},
		Pools:     []model.DHCPPool{{Name: "lan", Interface: "lan", Start: 100, Limit: 150, LeaseTime: "12h"}},
		Hosts:     []model.Host{{Name: "nas", MAC: "aa:bb:cc:dd:ee:ff", IP: netip.MustParseAddr("192.168.1.10")}},
		Radios:    []model.WifiDevice{{Name: "radio0", Band: "5g", Channel: "36"}},
		SSIDs:     []model.WifiIface{{Name: "ap0", Device: "radio0", Mode: "ap", SSID: "wash", Encryption: model.EncPSK2{Key: "secret"}}},
	}
	if ds := Validate(c, caps.Full()); errorCount(ds) != 0 {
		t.Fatalf("expected no errors, got %d: %+v", errorCount(ds), ds)
	}
}

func TestFieldChecks(t *testing.T) {
	cases := []struct {
		name     string
		cfg      model.Config
		wantCode string
		wantPath string
	}{
		{"missing iface name",
			model.Config{Interfaces: []model.Interface{{Proto: model.DHCPProto{}}}},
			"required", "Interfaces[0].Name"},
		{"static iface without ipaddr",
			model.Config{Interfaces: []model.Interface{{Name: "lan", Proto: model.StaticProto{}}}},
			"required", "Interfaces[0].Proto.IPAddr"},
		{"bad zone target",
			model.Config{Zones: []model.Zone{{Name: "z", Input: "ALLOW"}}},
			"invalid_enum", "Zones[0].Input"},
		{"bad redirect port",
			model.Config{Redirects: []model.Redirect{{Name: "x", Target: "DNAT", DestPort: "99999"}}},
			"invalid_port", "Redirects[0].DestPort"},
		{"port range ok then bad",
			model.Config{FwRules: []model.FirewallRule{{Target: "ACCEPT", DestPort: "1000-abc"}}},
			"invalid_port", "FwRules[0].DestPort"},
		{"bad mac",
			model.Config{Hosts: []model.Host{{Name: "h", MAC: "zz:zz"}}},
			"invalid_mac", "Hosts[0].MAC"},
		{"bad wifi mode",
			model.Config{SSIDs: []model.WifiIface{{Name: "a", Mode: "router"}}},
			"invalid_enum", "SSIDs[0].Mode"},
		{"bad band",
			model.Config{Radios: []model.WifiDevice{{Name: "r", Band: "10g"}}},
			"invalid_enum", "Radios[0].Band"},
		{"bad channel",
			model.Config{Radios: []model.WifiDevice{{Name: "r", Band: "5g", Channel: "thirtysix"}}},
			"invalid_channel", "Radios[0].Channel"},
		{"ap without ssid",
			model.Config{SSIDs: []model.WifiIface{{Name: "a", Mode: "ap"}}},
			"required", "SSIDs[0].SSID"},
		{"bad device type",
			model.Config{Devices: []model.Device{{Name: "x", Type: "switchport"}}},
			"invalid_enum", "Devices[0].Type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := Validate(tc.cfg, caps.Full())
			if find(ds, tc.wantCode, tc.wantPath) == nil {
				t.Fatalf("want diagnostic %s at %s; got %+v", tc.wantCode, tc.wantPath, ds)
			}
		})
	}
}

func TestCapabilityGating(t *testing.T) {
	// An NM-like workstation profile: interfaces + wifi client + wireguard, but
	// no zones, no DHCP server, no AP, no NAT.
	nm := caps.New(
		[]string{"network/interface", "network/wireguard_peer", "wireless/wifi-iface"},
		caps.CanWireGuard,
	)

	t.Run("unsupported kind", func(t *testing.T) {
		c := model.Config{Zones: []model.Zone{{Name: "lan", Input: "ACCEPT"}}}
		if find(Validate(c, nm), "unsupported_kind", "Zones[0]") == nil {
			t.Fatal("expected unsupported_kind for firewall zone under NM caps")
		}
	})

	t.Run("AP needs CanAP", func(t *testing.T) {
		c := model.Config{SSIDs: []model.WifiIface{{Name: "a", Mode: "ap", SSID: "x", Encryption: model.EncNone{}}}}
		if find(Validate(c, nm), "unsupported_feature", "SSIDs[0]") == nil {
			t.Fatal("expected unsupported_feature (AP) under NM caps")
		}
	})

	t.Run("wireguard allowed", func(t *testing.T) {
		c := model.Config{Interfaces: []model.Interface{{Name: "vpn", Proto: model.WireGuardProto{PrivateKey: "K"}}}}
		if d := find(Validate(c, nm), "unsupported_feature", "Interfaces[0]"); d != nil {
			t.Fatalf("wireguard should be allowed under NM caps, got %+v", d)
		}
	})

	t.Run("masq needs CanNAT", func(t *testing.T) {
		// zone kind is also unsupported here; assert the NAT feature diag specifically
		c := model.Config{Zones: []model.Zone{{Name: "wan", Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true}}}
		ds := Validate(c, nm)
		if find(ds, "unsupported_feature", "Zones[0]") == nil {
			t.Fatalf("expected unsupported_feature for zones/NAT, got %+v", ds)
		}
	})
}
