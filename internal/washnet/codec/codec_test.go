package codec

import (
	"fmt"
	"math/rand"
	"net/netip"
	"reflect"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// roundTrip is the A1 commit gate: Parse(Render(c)) == c.
func roundTrip(t *testing.T, c model.Config) {
	t.Helper()
	files, err := Render(c)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	got, err := Parse(files)
	if err != nil {
		t.Fatalf("Parse: %v\nrendered:\n%s", err, files)
	}
	if !reflect.DeepEqual(c, got) {
		t.Fatalf("round-trip mismatch\n want: %#v\n  got: %#v\nrendered:\n%s", c, got, files)
	}
}

func TestRoundTripTable(t *testing.T) {
	cases := map[string]model.Config{
		"empty": {},
		"static iface with dns": {
			Interfaces: []model.Interface{{
				Name:   "lan",
				Proto:  "static",
				Device: "br-lan",
				IPAddr: netip.MustParsePrefix("10.0.0.1/24"),
				DNS:    []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("8.8.8.8")},
			}},
		},
		"dhcp iface, no optional fields": {
			Interfaces: []model.Interface{{Name: "wan", Proto: "dhcp"}},
		},
		"zone with masq and networks": {
			Zones: []model.Zone{{
				Name:     "lan",
				Networks: []string{"lan", "guest"},
				Input:    "ACCEPT",
				Output:   "ACCEPT",
				Forward:  "REJECT",
				Masq:     false,
			}, {
				Name:    "wan",
				Input:   "REJECT",
				Output:  "ACCEPT",
				Forward: "REJECT",
				Masq:    true,
			}},
		},
		"interfaces and zones together": {
			Interfaces: []model.Interface{
				{Name: "wan", Proto: "dhcp", Device: "eth0"},
				{Name: "lan", Proto: "static", Device: "br-lan", IPAddr: netip.MustParsePrefix("192.168.1.1/24")},
			},
			Zones: []model.Zone{{Name: "wan", Networks: []string{"wan"}, Masq: true, Input: "DROP"}},
		},
		"ipv6 addr and dns": {
			Interfaces: []model.Interface{{
				Name:   "lan6",
				Proto:  "static",
				IPAddr: netip.MustParsePrefix("fd00::1/64"),
				DNS:    []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")},
			}},
		},
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

// --- generators (nil for empty so DeepEqual stays exact) -------------------

func genConfig(r *rand.Rand) model.Config {
	var c model.Config
	for i, n := 0, r.Intn(4); i < n; i++ {
		c.Interfaces = append(c.Interfaces, genInterface(r, i))
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Zones = append(c.Zones, genZone(r, i))
	}
	return c
}

func genInterface(r *rand.Rand, i int) model.Interface {
	in := model.Interface{
		Name:  fmt.Sprintf("if%d", i),
		Proto: choice(r, "static", "dhcp", "none"),
	}
	if r.Intn(2) == 0 {
		in.Device = fmt.Sprintf("eth%d", r.Intn(4))
	}
	if in.Proto == "static" {
		in.IPAddr = netip.PrefixFrom(randAddr4(r), 24)
	}
	for j, n := 0, r.Intn(3); j < n; j++ {
		in.DNS = append(in.DNS, randAddr4(r))
	}
	return in
}

func genZone(r *rand.Rand, i int) model.Zone {
	z := model.Zone{
		Name:    fmt.Sprintf("zone%d", i),
		Input:   choice(r, "ACCEPT", "REJECT", "DROP"),
		Output:  choice(r, "ACCEPT", "REJECT", "DROP"),
		Forward: choice(r, "ACCEPT", "REJECT", "DROP"),
		Masq:    r.Intn(2) == 0,
	}
	for j, n := 0, r.Intn(3); j < n; j++ {
		z.Networks = append(z.Networks, fmt.Sprintf("net%d", j))
	}
	return z
}

func randAddr4(r *rand.Rand) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(256)), byte(1 + r.Intn(254))})
}

func choice(r *rand.Rand, opts ...string) string { return opts[r.Intn(len(opts))] }
