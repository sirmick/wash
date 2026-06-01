package codec

import (
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// realisticConfig is a typical type-1 gateway: WAN+LAN+VPN, NAT firewall, DHCP,
// and an AP. It exercises every package and both unions, and doubles as the
// OpenWRT-fidelity golden (docs/NET.md §11 A2).
func realisticConfig() model.Config {
	return model.Config{
		Globals: []model.Globals{{Name: "globals", ULAPrefix: netip.MustParsePrefix("fdca:1234::/48")}},
		Interfaces: []model.Interface{
			{Name: "loopback", Device: "lo", Proto: model.StaticProto{IPAddr: netip.MustParsePrefix("127.0.0.1/8")}},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}},
			{Name: "lan", Device: "br-lan", Proto: model.StaticProto{
				IPAddr: netip.MustParsePrefix("192.168.1.1/24"),
				DNS:    []netip.Addr{netip.MustParseAddr("1.1.1.1")},
			}},
			{Name: "vpn", Proto: model.WireGuardProto{
				PrivateKey: "PRIVKEY=", ListenPort: 51820,
				Addresses: []netip.Prefix{netip.MustParsePrefix("10.9.0.1/24")},
			}},
		},
		Devices:    []model.Device{{Name: "br-lan", Type: "bridge", Ports: []string{"eth1", "eth2"}}},
		Routes:     []model.Route{{Interface: "lan", Target: netip.MustParsePrefix("10.10.0.0/16"), Gateway: netip.MustParseAddr("192.168.1.254"), Metric: 1}},
		WGPeers:    []model.WGPeer{{Name: "phone", Interface: "vpn", PublicKey: "PEERPUB=", AllowedIPs: []netip.Prefix{netip.MustParsePrefix("10.9.0.2/32")}, PersistentKeepalive: 25}},
		FwDefaults: []model.Defaults{{Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT", SynFlood: true}},
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true, MTUFix: true},
		},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
		Redirects:   []model.Redirect{{Name: "ssh", Src: "wan", SrcDPort: "2222", Dest: "lan", DestIP: netip.MustParseAddr("192.168.1.10"), DestPort: "22", Proto: "tcp", Target: "DNAT"}},
		FwRules:     []model.FirewallRule{{Name: "Allow-Ping", Src: "wan", Proto: "icmp", Target: "ACCEPT", Family: "ipv4"}},
		Dnsmasq:     []model.Dnsmasq{{DomainNeeded: true, BogusPriv: true, Local: "/lan/", Domain: "lan", ExpandHosts: true, Server: []string{"1.1.1.1", "8.8.8.8"}}},
		Pools:       []model.DHCPPool{{Name: "lan", Interface: "lan", Start: 100, Limit: 150, LeaseTime: "12h"}},
		Hosts:       []model.Host{{Name: "nas", MAC: "aa:bb:cc:dd:ee:ff", IP: netip.MustParseAddr("192.168.1.10"), Hostname: "nas"}},
		Radios:      []model.WifiDevice{{Name: "radio0", Type: "mac80211", Band: "5g", Channel: "36", HTMode: "HE80", Country: "US"}},
		SSIDs:       []model.WifiIface{{Name: "default_radio0", Device: "radio0", Mode: "ap", SSID: "wash", Network: "lan", Encryption: model.EncPSK2{Key: "supersecret"}}},
	}
}

// TestGoldenRender renders the realistic config and compares each package file
// to testdata/golden_<pkg>.uci. Regenerate with UPDATE_GOLDEN=1. It also asserts
// the realistic config round-trips.
func TestGoldenRender(t *testing.T) {
	c := realisticConfig()
	files, err := Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if got, err := Parse(files); err != nil {
		t.Fatalf("Parse: %v", err)
	} else if !reflect.DeepEqual(c, got) {
		t.Fatalf("realistic config round-trip mismatch:\n got %#v", got)
	}
	update := os.Getenv("UPDATE_GOLDEN") != ""
	if update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for pkg, text := range files {
		path := filepath.Join("testdata", "golden_"+pkg+".uci")
		if update {
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read golden %s (run UPDATE_GOLDEN=1 to create): %v", path, err)
		}
		if string(want) != text {
			t.Errorf("golden %q mismatch:\n--- want ---\n%s\n--- got ---\n%s", pkg, want, text)
		}
	}
}
