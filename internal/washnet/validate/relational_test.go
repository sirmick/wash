package validate

import (
	"net/netip"
	"testing"

	"github.com/sirmick/wash/internal/washnet/caps"
	"github.com/sirmick/wash/internal/washnet/model"
)

func TestRelationalInvariants(t *testing.T) {
	cases := []struct {
		name     string
		cfg      model.Config
		wantCode string
		wantPath string
	}{
		{"duplicate interface name",
			model.Config{Interfaces: []model.Interface{
				{Name: "lan", Proto: model.DHCPProto{}},
				{Name: "lan", Proto: model.DHCPProto{}},
			}},
			"duplicate_name", "Interfaces[1].Name"},

		{"forwarding to unknown zone",
			model.Config{
				Zones:       []model.Zone{{Name: "lan", Input: "ACCEPT"}},
				Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
			},
			"unknown_zone", "Forwardings[0].Dest"},

		{"zone references unknown network",
			model.Config{Zones: []model.Zone{{Name: "lan", Networks: []string{"nope"}, Input: "ACCEPT"}}},
			"unknown_network", "Zones[0].Networks"},

		{"ssid references unknown radio",
			model.Config{SSIDs: []model.WifiIface{{Name: "a", Device: "radio9", Mode: "sta"}}},
			"unknown_radio", "SSIDs[0].Device"},

		{"wgpeer interface not wireguard",
			model.Config{
				Interfaces: []model.Interface{{Name: "lan", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("10.0.0.1/24")}}}},
				WGPeers:    []model.WGPeer{{Name: "p", Interface: "lan", PublicKey: "K"}},
			},
			"not_wireguard", "WGPeers[0].Interface"},

		{"wgpeer unknown interface",
			model.Config{WGPeers: []model.WGPeer{{Name: "p", Interface: "ghost", PublicKey: "K"}}},
			"unknown_network", "WGPeers[0].Interface"},

		{"overlapping static subnets",
			model.Config{Interfaces: []model.Interface{
				{Name: "lan", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("192.168.0.1/16")}}},
				{Name: "lan2", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("192.168.1.1/24")}}},
			}},
			"subnet_overlap", "Interfaces[1].IPAddr"},

		{"wireguard allowed_ips overlap",
			model.Config{
				Interfaces: []model.Interface{{Name: "vpn", Proto: model.WireGuardProto{PrivateKey: "K"}}},
				WGPeers: []model.WGPeer{
					{Name: "a", Interface: "vpn", PublicKey: "A", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.0/24")}},
					{Name: "b", Interface: "vpn", PublicKey: "B", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.5/32")}},
				},
			},
			"allowedips_overlap", "WGPeers[1].AllowedIPs"},

		{"bridge port conflict",
			model.Config{Devices: []model.Device{
				{Name: "br0", Type: "bridge", Ports: []string{"eth1"}},
				{Name: "br1", Type: "bridge", Ports: []string{"eth1"}},
			}},
			"port_conflict", "Devices[1].Ports"},

		{"duplicate static lease MAC",
			model.Config{Hosts: []model.Host{
				{Name: "a", MAC: "AA:BB:CC:DD:EE:FF"},
				{Name: "b", MAC: "aa:bb:cc:dd:ee:ff"},
			}},
			"duplicate_mac", "Hosts[1].MAC"},

		{"duplicate static lease IP",
			model.Config{Hosts: []model.Host{
				{Name: "a", IP: netip.MustParseAddr("192.168.1.10")},
				{Name: "b", IP: netip.MustParseAddr("192.168.1.10")},
			}},
			"duplicate_ip", "Hosts[1].IP"},

		{"pool references unknown interface",
			model.Config{Pools: []model.DHCPPool{{Name: "ghost", Start: 100, Limit: 50}}},
			"unknown_network", "Pools[0].Interface"},

		{"shadowed firewall rule",
			model.Config{FwRules: []model.FirewallRule{
				{Name: "drop-ssh", Src: "wan", Proto: "tcp", DestPort: "22", Target: "DROP"},
				{Name: "allow-ssh", Src: "wan", Proto: "tcp", DestPort: "22", Target: "ACCEPT"},
			}},
			"shadowed_rule", "FwRules[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ds := Validate(tc.cfg, caps.Full())
			if find(ds, tc.wantCode, tc.wantPath) == nil {
				t.Fatalf("want %s at %s; got %+v", tc.wantCode, tc.wantPath, ds)
			}
		})
	}
}

// TestRelationalCleanConfig: a coherent gateway has no relational findings.
func TestRelationalCleanConfig(t *testing.T) {
	c := model.Config{
		Interfaces: []model.Interface{
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{IPAddr: []netip.Prefix{netip.MustParsePrefix("192.168.1.1/24")}}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
			{Name: "vpn", Proto: model.WireGuardProto{PrivateKey: "K"}},
		},
		Devices:     []model.Device{{Name: "br-lan", Type: "bridge", Ports: []string{"eth1", "eth2"}}},
		Zones:       []model.Zone{{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"}, {Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true}},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
		Pools:       []model.DHCPPool{{Name: "lan", Interface: "lan", Start: 100, Limit: 150}},
		WGPeers:     []model.WGPeer{{Name: "phone", Interface: "vpn", PublicKey: "P", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.2/32")}}},
		Radios:      []model.WifiDevice{{Name: "radio0", Band: "5g", Channel: "36"}},
		SSIDs:       []model.WifiIface{{Name: "ap0", Device: "radio0", Network: "lan", Mode: "ap", SSID: "wash", Encryption: model.EncPSK2{Key: "secret"}}},
	}
	if ds := Validate(c, caps.Full()); errorCount(ds) != 0 {
		t.Fatalf("expected no errors, got %d: %+v", errorCount(ds), ds)
	}
}
