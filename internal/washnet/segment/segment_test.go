package segment

import (
	"fmt"
	"math/rand"
	"net/netip"
	"reflect"
	"sort"
	"testing"

	"github.com/sirmick/wash/internal/washnet/model"
)

// canon sorts every slice in a Config by a total %+v key so round-trip equality
// is judged as a multiset per kind — order within a kind isn't semantically
// meaningful (firewall-rule order is checked separately, TestPolicyOrderPreserved).
// Reflection-driven so it covers every Config field, including ones added later.
func canon(c model.Config) model.Config {
	v := reflect.ValueOf(&c).Elem()
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if f.Kind() == reflect.Slice && f.Len() > 1 {
			sort.SliceStable(f.Interface(), func(a, b int) bool {
				return fmt.Sprintf("%+v", f.Index(a).Interface()) <
					fmt.Sprintf("%+v", f.Index(b).Interface())
			})
		}
	}
	return c
}

// roundTrip is the lens commit gate: canon(Materialize(Project(c))) == canon(c).
func roundTrip(t *testing.T, c model.Config) {
	t.Helper()
	got := Materialize(Project(c))
	if !reflect.DeepEqual(canon(got), canon(c)) {
		t.Fatalf("round-trip mismatch\n want: %#v\n  got: %#v", c, got)
	}
}

func TestRoundTripProperty(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	for i := 0; i < 500; i++ {
		roundTrip(t, genConfig(r))
	}
}

// TestRoundTripHarborShape projects a config shaped like the harbor fixture — VLAN
// LAN segments + a masquerading WAN + a WireGuard VPN egress + an inter-segment
// firewall matrix — and asserts the decomposition (roles, carriers, ownership)
// before confirming it materializes back exactly.
func TestRoundTripHarborShape(t *testing.T) {
	c := harborShape()
	p := Project(c)

	if len(p.Segments) != 5 {
		t.Fatalf("want 5 segments (lo + iot + cam + wan + vpn), got %d", len(p.Segments))
	}
	byName := map[string]Segment{}
	for _, s := range p.Segments {
		byName[s.Name] = s
	}

	// iot: a VLAN LAN segment with a zone + a DHCP pool that hands out its own DNS.
	iot := byName["iot"]
	if got := iot.Role(); got != RoleLAN {
		t.Errorf("iot role = %q, want lan", got)
	}
	if car := iot.Carrier(); car.Kind != CarrierVLAN || car.VID != 6 || car.Port != "switch" {
		t.Errorf("iot carrier = %+v, want vlan 6 on switch", car)
	}
	if iot.Zone == nil || iot.Pool == nil {
		t.Fatalf("iot should own a zone + pool, got zone=%v pool=%v", iot.Zone, iot.Pool)
	}
	if len(iot.Pool.DHCPOption) == 0 {
		t.Errorf("iot pool should carry its per-segment DHCP DNS option")
	}

	// wan: masquerading uplink. vpn: the WireGuard egress.
	if got := byName["wan"].Role(); got != RoleWAN {
		t.Errorf("wan role = %q, want wan", got)
	}
	if got := byName["vpn"].Role(); got != RoleVPN {
		t.Errorf("vpn role = %q, want vpn", got)
	}

	// The firewall matrix is policy, not segment-local.
	if len(p.Policy.Forwardings) != 2 || len(p.Policy.Defaults) != 1 {
		t.Errorf("policy = %d forwardings / %d defaults, want 2 / 1", len(p.Policy.Forwardings), len(p.Policy.Defaults))
	}

	roundTrip(t, c)
}

// TestPolicyOrderPreserved: fw4 evaluates rules top-down, so the lens must keep
// firewall-rule and forwarding order intact end to end (canon would hide a
// reorder, so assert the raw order here).
func TestPolicyOrderPreserved(t *testing.T) {
	c := model.Config{
		FwRules: []model.FirewallRule{
			{Name: "first", Src: "lan", Dest: "wan", Target: "ACCEPT"},
			{Name: "second", Src: "guest", Dest: "wan", Target: "REJECT"},
			{Name: "third", Src: "iot", Dest: "wan", Target: "DROP"},
		},
		Forwardings: []model.Forwarding{{Src: "lan", Dest: "wan"}, {Src: "guest", Dest: "wan"}},
	}
	got := Materialize(Project(c))
	if !reflect.DeepEqual(got.FwRules, c.FwRules) {
		t.Errorf("firewall-rule order not preserved:\n got %+v\nwant %+v", got.FwRules, c.FwRules)
	}
	if !reflect.DeepEqual(got.Forwardings, c.Forwardings) {
		t.Errorf("forwarding order not preserved:\n got %+v\nwant %+v", got.Forwardings, c.Forwardings)
	}
}

// TestLeftoversQuarantine: kinds the lens doesn't absorb (routes, ipsets, a
// multi-network zone, a shared device) land in Leftovers untouched and round-trip.
func TestLeftoversQuarantine(t *testing.T) {
	c := model.Config{
		Interfaces: []model.Interface{{Name: "lan", Device: "br-lan", Proto: staticV4("10.0.0.1/24")}},
		Devices:    []model.Device{{Name: "br-lan", Type: "bridge", Ports: []string{"eth0"}}}, // absorbed by the lan segment
		Zones: []model.Zone{
			{Name: "lan", Networks: []string{"lan"}, Input: "ACCEPT"},
			{Name: "trusted", Networks: []string{"lan", "lan2"}, Input: "ACCEPT"}, // multi-network → leftover
		},
		Routes:  []model.Route{{Target: netip.MustParsePrefix("0.0.0.0/0"), Table: "vpn", Type: "blackhole"}},
		IPSets:  []model.IPSet{{Name: "rfc1918", Match: "src_net", Entry: []string{"10.0.0.0/8"}}},
		Domains: []model.Domain{{Name: "nas", IP: netip.MustParseAddr("10.0.0.5")}},
	}
	p := Project(c)
	if len(p.Leftovers.Routes) != 1 || len(p.Leftovers.IPSets) != 1 || len(p.Leftovers.Domains) != 1 {
		t.Errorf("routes/ipsets/domains should be quarantined in leftovers: %+v", p.Leftovers)
	}
	// The multi-network zone is not segment-owned → leftover; the single-network one is.
	if len(p.Leftovers.Zones) != 1 || p.Leftovers.Zones[0].Name != "trusted" {
		t.Errorf("multi-network zone should be a leftover, got %+v", p.Leftovers.Zones)
	}
	roundTrip(t, c)
}

// --- fixtures + generator --------------------------------------------------

func staticV4(cidrs ...string) model.StaticProto {
	var sp model.StaticProto
	for _, c := range cidrs {
		sp.IPAddr = append(sp.IPAddr, netip.MustParsePrefix(c))
	}
	return sp
}

// harborShape is a compact stand-in for the harbor topology: loopback, two VLAN
// LAN segments (one with a per-segment DHCP DNS option), a masquerading WAN, a
// WireGuard VPN egress, plus a small firewall matrix.
func harborShape() model.Config {
	return model.Config{
		Interfaces: []model.Interface{
			{Name: "loopback", Device: "lo", Proto: staticV4("127.0.0.1/8")},
			{Name: "iot", Device: "iot.6", Proto: staticV4("192.168.15.1/24")},
			{Name: "cam", Device: "cam.3", Proto: staticV4("192.168.3.1/24")},
			{Name: "wan", Device: "eth0", Proto: model.DHCPProto{IPv4: true}},
			{Name: "vpn", Proto: model.WireGuardProto{PrivateKey: "K", ListenPort: 51820,
				Addresses: []netip.Prefix{netip.MustParsePrefix("10.6.0.1/24")}}},
		},
		Devices: []model.Device{
			{Name: "iot.6", Type: "8021q", Ifname: "switch", VID: 6},
			{Name: "cam.3", Type: "8021q", Ifname: "switch", VID: 3},
		},
		FwDefaults: []model.Defaults{{Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"}},
		Zones: []model.Zone{
			{Name: "iot", Networks: []string{"iot"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"},
			{Name: "cam", Networks: []string{"cam"}, Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"},
			{Name: "wan", Networks: []string{"wan"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
			{Name: "vpn", Networks: []string{"vpn"}, Input: "REJECT", Output: "ACCEPT", Forward: "REJECT", Masq: true},
		},
		Forwardings: []model.Forwarding{{Src: "iot", Dest: "wan"}, {Src: "cam", Dest: "vpn"}},
		Pools: []model.DHCPPool{
			{Name: "iot", Interface: "iot", Start: 50, Limit: 150, LeaseTime: "12h", DHCPOption: []string{"6,192.168.15.1"}},
			{Name: "cam", Interface: "cam", Start: 5, Limit: 200, LeaseTime: "12h"},
		},
	}
}

func genConfig(r *rand.Rand) model.Config {
	var c model.Config
	for i, n := 0, r.Intn(5); i < n; i++ {
		name := fmt.Sprintf("seg%d", i)
		iface := model.Interface{Name: name, Proto: genProto(r, i)}
		switch r.Intn(3) { // carrier: raw / vlan / bridge
		case 0:
			iface.Device = fmt.Sprintf("eth%d", i)
		case 1:
			dev := fmt.Sprintf("v%d", i)
			iface.Device = dev
			c.Devices = append(c.Devices, model.Device{Name: dev, Type: "8021q", Ifname: "switch", VID: 10 + i})
		case 2:
			dev := fmt.Sprintf("br%d", i)
			iface.Device = dev
			c.Devices = append(c.Devices, model.Device{Name: dev, Type: "bridge", Ports: []string{fmt.Sprintf("eth%d", i)}})
		}
		c.Interfaces = append(c.Interfaces, iface)
		if r.Intn(2) == 0 {
			z := model.Zone{Name: name, Networks: []string{name}, Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"}
			z.Masq = r.Intn(2) == 0
			c.Zones = append(c.Zones, z)
		}
		if r.Intn(2) == 0 {
			p := model.DHCPPool{Name: name, Interface: name, Start: r.Intn(200), Limit: r.Intn(200), LeaseTime: "12h"}
			if r.Intn(2) == 0 {
				p.DHCPOption = []string{"6," + randAddr(r)}
			}
			c.Pools = append(c.Pools, p)
		}
	}
	// firewall matrix
	if r.Intn(2) == 0 {
		c.FwDefaults = []model.Defaults{{Input: "ACCEPT", Output: "ACCEPT", Forward: "REJECT"}}
	}
	for i, n := 0, r.Intn(4); i < n; i++ {
		c.Forwardings = append(c.Forwardings, model.Forwarding{Src: fmt.Sprintf("seg%d", i), Dest: "wan"})
	}
	for i, n := 0, r.Intn(4); i < n; i++ {
		c.FwRules = append(c.FwRules, model.FirewallRule{Name: fmt.Sprintf("r%d", i), Src: "seg0", Dest: "wan", Target: "ACCEPT"})
	}
	// leftover kinds (must survive untouched)
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Routes = append(c.Routes, model.Route{Interface: "seg0", Target: netip.PrefixFrom(randAddr4(r), 16), Gateway: randAddr4(r)})
	}
	for i, n := 0, r.Intn(3); i < n; i++ {
		c.Hosts = append(c.Hosts, model.Host{Name: fmt.Sprintf("h%d", i), IP: randAddr4(r)})
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		c.IPSets = append(c.IPSets, model.IPSet{Name: fmt.Sprintf("set%d", i), Match: "src_net", Entry: []string{"10.0.0.0/8"}})
	}
	for i, n := 0, r.Intn(2); i < n; i++ {
		c.Domains = append(c.Domains, model.Domain{Name: fmt.Sprintf("d%d", i), IP: randAddr4(r)})
	}
	return c
}

func genProto(r *rand.Rand, i int) model.ProtoConfig {
	switch r.Intn(4) {
	case 0:
		return staticV4(fmt.Sprintf("10.0.%d.1/24", i))
	case 1:
		return model.DHCPProto{IPv4: true}
	case 2:
		return model.WireGuardProto{PrivateKey: "K", ListenPort: 1 + r.Intn(60000),
			Addresses: []netip.Prefix{netip.PrefixFrom(randAddr4(r), 24)}}
	default:
		return model.NoneProto{}
	}
}

func randAddr4(r *rand.Rand) netip.Addr {
	return netip.AddrFrom4([4]byte{byte(r.Intn(254) + 1), byte(r.Intn(256)), byte(r.Intn(256)), byte(r.Intn(254) + 1)})
}

func randAddr(r *rand.Rand) string { return randAddr4(r).String() }
