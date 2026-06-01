package nmprofile

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"net/netip"

	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
)

var update = flag.Bool("update", false, "rewrite .nmconnection golden files")

// Each corpus scenario is a directory under testdata/ holding:
//
//	<package>.uci           canonical source, one file per UCI package
//	                        (network.uci, wireless.uci, …) — the IR's input form
//	<connID>.nmconnection   the rendered NM connection set (one file per conn)
//
// covering the cases that exercise the model↔NM shape gap: eth, bridge, vlan,
// wifi, wireguard.
func scenarios(t *testing.T) []string {
	t.Helper()
	dirs, err := filepath.Glob("testdata/*")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, filepath.Base(d))
		}
	}
	sort.Strings(out)
	return out
}

// readSet reads every *.nmconnection in a scenario dir, keyed by connection id
// (the filename stem == the keyfile's [connection] id).
func readSet(t *testing.T, dir string) map[string]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", dir, "*.nmconnection"))
	set := map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		set[strings.TrimSuffix(filepath.Base(f), ".nmconnection")] = string(b)
	}
	return set
}

// TestCompile is the "compile to NM profiles" gate: canonical UCI → IR →
// NM keyfile set must equal the goldens. -update regenerates them.
func TestCompile(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			// Source is per-UCI-package files (network.uci, wireless.uci, …).
			uciFiles, _ := filepath.Glob(filepath.Join("testdata", name, "*.uci"))
			pkgs := map[string]string{}
			for _, f := range uciFiles {
				b, err := os.ReadFile(f)
				if err != nil {
					t.Fatal(err)
				}
				pkgs[strings.TrimSuffix(filepath.Base(f), ".uci")] = string(b)
			}
			cfg, err := codec.Parse(pkgs)
			if err != nil {
				t.Fatalf("parse uci: %v", err)
			}
			got, err := RenderKeyfiles(cfg)
			if err != nil {
				t.Fatalf("render keyfiles: %v", err)
			}
			if *update {
				for id, text := range got {
					p := filepath.Join("testdata", name, id+".nmconnection")
					if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			assertSet(t, got, readSet(t, name))
		})
	}
}

// TestRoundTrip is the fidelity gate you care about: NM in → IR → NM out must
// be identical to NM in, for every connection in every scenario. This is what
// makes "load the box's current NM state, edit it, write it back" lossless.
func TestRoundTrip(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			in := readSet(t, name)
			if len(in) == 0 {
				t.Skip("no .nmconnection fixtures yet")
			}
			cfg, err := ParseKeyfiles(in)
			if err != nil {
				t.Fatalf("parse keyfiles → IR: %v", err)
			}
			out, err := RenderKeyfiles(cfg)
			if err != nil {
				t.Fatalf("render IR → keyfiles: %v", err)
			}
			assertSet(t, out, in)
		})
	}
}

func assertSet(t *testing.T, got, want map[string]string) {
	t.Helper()
	for id, w := range want {
		g, ok := got[id]
		if !ok {
			t.Errorf("missing connection %q in output", id)
			continue
		}
		if g != w {
			t.Errorf("connection %q mismatch\n--- got ---\n%s\n--- want ---\n%s", id, g, w)
		}
	}
	for id := range got {
		if _, ok := want[id]; !ok {
			t.Errorf("unexpected extra connection %q\n%s", id, got[id])
		}
	}
}

// TestIPv6DualStack locks in the IPv6 addressing path: a static interface with
// both v4 and v6 addresses + mixed DNS renders manual [ipv4] AND [ipv6], and
// round-trips back to the same model (NM keyfiles → model → keyfiles).
func TestIPv6DualStack(t *testing.T) {
	cfg := model.Config{
		Interfaces: []model.Interface{{
			Name:   "wan",
			Device: "eth0",
			Proto: model.StaticProto{
				IPAddr:  netip.MustParsePrefix("192.0.2.10/24"),
				Gateway: netip.MustParseAddr("192.0.2.1"),
				IP6Addr: netip.MustParsePrefix("2001:db8::10/64"),
				IP6Gw:   netip.MustParseAddr("2001:db8::1"),
				DNS:     []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2606:4700:4700::1111")},
			},
		}},
	}
	kfs, err := RenderKeyfiles(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wan := kfs["wan"]
	for _, want := range []string{
		"[ipv4]", "address1=192.0.2.10/24,192.0.2.1", "dns=1.1.1.1;",
		"[ipv6]", "method=manual", "address1=2001:db8::10/64,2001:db8::1", "dns=2606:4700:4700::1111;",
	} {
		if !strings.Contains(wan, want) {
			t.Errorf("rendered wan missing %q:\n%s", want, wan)
		}
	}

	cfg2, err := ParseKeyfiles(kfs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	sp, ok := cfg2.Interfaces[0].Proto.(model.StaticProto)
	if !ok {
		t.Fatalf("proto = %T, want StaticProto", cfg2.Interfaces[0].Proto)
	}
	if sp.IP6Addr.String() != "2001:db8::10/64" || sp.IP6Gw.String() != "2001:db8::1" {
		t.Errorf("ipv6 round-trip lost address/gw: %+v", sp)
	}
	if len(sp.DNS) != 2 {
		t.Errorf("DNS round-trip = %v, want both families", sp.DNS)
	}
}

// TestDHCPFamilies locks in per-family DHCP: a v4-only DHCP renders [ipv4] auto +
// [ipv6] disabled and round-trips, and a bare dhcp (no family flags) means both.
func TestDHCPFamilies(t *testing.T) {
	v4only := model.Config{Interfaces: []model.Interface{{Name: "wan", Device: "eth0", Proto: model.DHCPProto{IPv4: true}}}}
	kfs, err := RenderKeyfiles(v4only)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wan := kfs["wan"]
	if !strings.Contains(wan, "[ipv4]\nmethod=auto") || !strings.Contains(wan, "[ipv6]\nmethod=disabled") {
		t.Errorf("v4-only dhcp wrong sections:\n%s", wan)
	}
	cfg2, err := ParseKeyfiles(kfs)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	dp, ok := cfg2.Interfaces[0].Proto.(model.DHCPProto)
	if !ok || !dp.IPv4 || dp.IPv6 {
		t.Errorf("v4-only round-trip = %+v (want IPv4 only)", cfg2.Interfaces[0].Proto)
	}

	// Bare dhcp (no flags) ⇒ both families auto.
	both, _ := RenderKeyfiles(model.Config{Interfaces: []model.Interface{{Name: "w", Device: "eth0", Proto: model.DHCPProto{}}}})
	if !strings.Contains(both["w"], "[ipv4]\nmethod=auto") || !strings.Contains(both["w"], "[ipv6]\nmethod=auto") {
		t.Errorf("bare dhcp should be both-auto:\n%s", both["w"])
	}
}
