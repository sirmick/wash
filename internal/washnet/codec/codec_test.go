package codec

import (
	"fmt"
	"math/rand"
	"net/netip"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// roundTrip is the core codec commit gate: Parse(Render(c)) == c.
func roundTrip(t *testing.T, c model.Config) {
	t.Helper()
	files, err := Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := Parse(files)
	if err != nil {
		t.Fatalf("Parse: %v\nrendered:\n%v", err, files)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round-trip mismatch\n want: %#v\n  got: %#v\nrendered:\n%v", c, got, files)
	}
}

func TestRoundTripTable(t *testing.T) {
	cases := map[string]model.Config{
		"empty": {},
		"static iface with dns": {Interfaces: []model.Interface{{
			Name: "lan", Device: "br-lan",
			Proto: model.StaticProto{
				IPAddr: netip.MustParsePrefix("10.0.0.1/24"),
				DNS:    []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")},
			},
		}}},
		"dhcp iface": {Interfaces: []model.Interface{{Name: "wan", Device: "eth0", Proto: model.DHCPProto{}}}},
		"pppoe iface": {Interfaces: []model.Interface{{
			Name: "wan", Proto: model.PPPoEProto{Username: "u", Password: "p"},
		}}},
		"wireguard iface": {Interfaces: []model.Interface{{
			Name:  "vpn",
			Proto: model.WireGuardProto{PrivateKey: "K", ListenPort: 51820, Addresses: []netip.Prefix{netip.MustParsePrefix("10.9.0.1/24")}},
		}}},
		"zones + forwarding": {
			Zones: []model.Zone{
				{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "ACCEPT"},
				{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
			},
			Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}},
		},
		"port forward": {Redirects: []model.Redirect{{
			Name: "ssh", Src: "wan", SrcDPort: "2222", Dest: "lan",
			DestIP: netip.MustParseAddr("192.168.1.10"), DestPort: "22", Proto: "tcp", Target: "DNAT",
		}}},
		"dhcp pool + host": {
			Pools: []model.DHCPPool{{Name: "lan", Interface: "lan", Start: 100, Limit: 150, LeaseTime: "12h"}},
			Hosts: []model.Host{{Name: "nas", MAC: "aa:bb:cc:dd:ee:ff", IP: netip.MustParseAddr("192.168.1.10")}},
		},
		"pool with per-segment dns + ntp option": {
			Pools: []model.DHCPPool{{
				Name: "iot", Interface: "iot", Start: 50, Limit: 150, LeaseTime: "12h",
				DHCPOption: []string{"6,192.168.15.1", "42,192.168.15.1"},
			}},
		},
		"blackhole kill-switch route": {
			Routes: []model.Route{
				{Interface: "wg", Target: netip.MustParsePrefix("0.0.0.0/0"), Table: "vpn"},
				{Target: netip.MustParsePrefix("0.0.0.0/0"), Table: "vpn", Type: "blackhole", Metric: 100},
			},
		},
		"wifi psk2": {
			Radios: []model.WifiDevice{{Name: "radio0", Type: "mac80211", Band: "5g", Channel: "36", Country: "US"}},
			SSIDs:  []model.WifiIface{{Name: "ap0", Device: "radio0", Mode: "ap", SSID: "wash", Network: "lan", Encryption: model.EncPSK2{Key: "secret"}}},
		},
		"wifi open": {SSIDs: []model.WifiIface{{Name: "guest", Device: "radio0", Mode: "ap", SSID: "guest", Encryption: model.EncNone{}}}},
		"ipv6 ula":  {Globals: []model.Globals{{Name: "globals", ULAPrefix: netip.MustParsePrefix("fdca::/48")}}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) { roundTrip(t, c) })
	}
}

func TestRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1)) // deterministic
	for i := 0; i < 500; i++ {
		c := genConfig(r)
		t.Run(fmt.Sprintf("cfg%d", i), func(t *testing.T) { roundTrip(t, c) })
	}
}

func TestParseRejectsBadDatatypes(t *testing.T) {
	bad := map[string]map[string]string{
		"bad ipaddr in static proto": {"network": "config interface 'x'\n\toption proto 'static'\n\toption ipaddr 'banana'\n"},
		"bad int start":              {"dhcp": "config dhcp 'lan'\n\toption start 'abc'\n"},
		"bad host ip":                {"dhcp": "config host\n\toption ip 'nope'\n"},
		"unknown proto variant":      {"network": "config interface 'x'\n\toption proto 'frobnicate'\n"},
		"bad allowed_ips prefix":     {"network": "config interface 'x'\n\toption proto 'wireguard'\n\tlist addresses 'notaprefix'\n"},
	}
	for name, files := range bad {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(files); err == nil {
				t.Fatalf("expected parse error, got nil")
			}
		})
	}
}

// --- generators (nil for empty so DeepEqual stays exact) -------------------

func genConfig(r *rand.Rand) model.Config {
	var c model.Config
	for i, n := 0, r.Intn(4); i < n; i++ {
		c.Interfaces = append(c.Interfaces, genInterface(r, i))
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Devices = append(c.Devices, model.Device{
			Name: fmt.Sprintf("br%d", i), Type: "bridge", Ports: genStrs(r, "eth"), MTU: r.Intn(2) * 1500,
		})
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		if r.Intn(3) == 0 { // a blackhole route: no gateway, special type
			c.Routes = append(c.Routes, model.Route{
				Target: netip.PrefixFrom(randAddr4(r), 16), Table: "vpn", Type: "blackhole", Metric: r.Intn(10),
			})
			continue
		}
		c.Routes = append(c.Routes, model.Route{
			Interface: "lan", Target: netip.PrefixFrom(randAddr4(r), 16), Gateway: randAddr4(r), Metric: r.Intn(10),
		})
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Zones = append(c.Zones, genZone(r, i))
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		c.Redirects = append(c.Redirects, model.Redirect{
			Name: fmt.Sprintf("fwd%d", i), Src: "wan", SrcDPort: fmt.Sprint(1000 + r.Intn(60000)),
			Dest: "lan", DestIP: randAddr4(r), DestPort: fmt.Sprint(1 + r.Intn(60000)),
			Proto: choice(r, "tcp", "udp"), Target: "DNAT",
		})
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		p := model.DHCPPool{
			Name: fmt.Sprintf("pool%d", i), Interface: "lan", Start: r.Intn(200), Limit: r.Intn(200), LeaseTime: "12h",
		}
		if r.Intn(2) == 0 {
			p.DHCPOption = []string{"6," + randAddr4(r).String()}
		}
		c.Pools = append(c.Pools, p)
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Hosts = append(c.Hosts, model.Host{
			Name: fmt.Sprintf("h%d", i), MAC: randMAC(r), IP: randAddr4(r), Hostname: fmt.Sprintf("h%d", i),
		})
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		c.Radios = append(c.Radios, model.WifiDevice{
			Name: fmt.Sprintf("radio%d", i), Type: "mac80211", Band: choice(r, "2g", "5g"), Channel: "auto", Country: "US",
		})
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.SSIDs = append(c.SSIDs, model.WifiIface{
			Name: fmt.Sprintf("ap%d", i), Device: "radio0", Mode: "ap", SSID: fmt.Sprintf("net%d", i),
			Network: "lan", Encryption: genEnc(r), Hidden: r.Intn(2) == 0,
		})
	}
	return c
}

func genInterface(r *rand.Rand, i int) model.Interface {
	in := model.Interface{Name: fmt.Sprintf("if%d", i), Proto: genProto(r)}
	if r.Intn(2) == 0 {
		in.Device = fmt.Sprintf("eth%d", r.Intn(4))
	}
	return in
}

func genProto(r *rand.Rand) model.ProtoConfig {
	switch r.Intn(6) {
	case 0:
		return model.NoneProto{}
	case 1:
		p := model.StaticProto{IPAddr: netip.PrefixFrom(randAddr4(r), 24)}
		if r.Intn(2) == 0 {
			p.Gateway = randAddr4(r)
		}
		for j, n := 0, r.Intn(3); j < n; j++ {
			p.DNS = append(p.DNS, randAddr4(r))
		}
		return p
	case 2:
		if r.Intn(2) == 0 {
			return model.DHCPProto{Hostname: "host"}
		}
		return model.DHCPProto{}
	case 3:
		return model.NoneProto{}
	case 4:
		return model.PPPoEProto{Username: "user", Password: "pass"}
	default:
		p := model.WireGuardProto{PrivateKey: "KEY", ListenPort: 1 + r.Intn(60000)}
		for j, n := 0, 1+r.Intn(2); j < n; j++ {
			p.Addresses = append(p.Addresses, netip.PrefixFrom(randAddr4(r), 24))
		}
		return p
	}
}

func genEnc(r *rand.Rand) model.Encryption {
	switch r.Intn(3) {
	case 0:
		return model.EncNone{}
	case 1:
		return model.EncPSK2{Key: "secret123"}
	default:
		return model.EncSAE{Key: "secret123"}
	}
}

func genZone(r *rand.Rand, i int) model.Zone {
	return model.Zone{
		Name: fmt.Sprintf("zone%d", i), Networks: genStrs(r, "net"),
		Input: choice(r, "ACCEPT", "REJECT", "DROP"), Output: choice(r, "ACCEPT", "REJECT", "DROP"),
		Forward: choice(r, "ACCEPT", "REJECT", "DROP"), Masq: r.Intn(2) == 0, MTUFix: r.Intn(2) == 0,
	}
}

func genStrs(r *rand.Rand, prefix string) []string {
	var out []string
	for j, n := 0, r.Intn(3); j < n; j++ {
		out = append(out, fmt.Sprintf("%s%d", prefix, j))
	}
	return out
}

func randAddr4(r *rand.Rand) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), byte(1 + r.Intn(254))})
}

func randMAC(r *rand.Rand) string {
	return fmt.Sprintf("%02x:%02x:%02x:%02x:%02x:%02x", r.Intn(256), r.Intn(256), r.Intn(256), r.Intn(256), r.Intn(256), r.Intn(256))
}

func choice(r *rand.Rand, opts ...string) string { return opts[r.Intn(len(opts))] }
