package networkdprofile

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

var update = flag.Bool("update", false, "rewrite .netdev/.network golden files")

// Each corpus scenario is a directory under testdata/ holding:
//
//	<package>.uci                  canonical source, one per UCI package (the
//	                               backend-agnostic IR input — identical to the
//	                               nmprofile corpus, so both renderers are proven
//	                               against the same configs)
//	<prio>-<key>.netdev/.network   the rendered networkd unit set
//
// networkd covers the IP layer only (§2.3): eth, bridge, vlan, wireguard — no
// wifi scenario (that's the NM/hostapd backends).
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

// readSet reads every *.netdev and *.network in a scenario dir, keyed by filename.
func readSet(t *testing.T, dir string) map[string]string {
	t.Helper()
	set := map[string]string{}
	for _, ext := range []string{"*.netdev", "*.network"} {
		files, _ := filepath.Glob(filepath.Join("testdata", dir, ext))
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			set[filepath.Base(f)] = string(b)
		}
	}
	return set
}

// TestCompile is the "compile to networkd units" gate: canonical UCI → IR →
// networkd unit set must equal the goldens. -update regenerates them.
func TestCompile(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
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
			got, err := RenderUnits(cfg)
			if err != nil {
				t.Fatalf("render units: %v", err)
			}
			if *update {
				for fn, text := range got {
					if err := os.WriteFile(filepath.Join("testdata", name, fn), []byte(text), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				return
			}
			assertSet(t, got, readSet(t, name))
		})
	}
}

// TestRoundTrip is the fidelity gate: networkd units in → IR → networkd units
// out must be identical, for every scenario. This is what makes "load current
// networkd state, edit it, write it back" lossless.
func TestRoundTrip(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			in := readSet(t, name)
			if len(in) == 0 {
				t.Skip("no unit fixtures yet")
			}
			cfg, err := ParseUnits(in)
			if err != nil {
				t.Fatalf("parse units → IR: %v", err)
			}
			out, err := RenderUnits(cfg)
			if err != nil {
				t.Fatalf("render IR → units: %v", err)
			}
			assertSet(t, out, in)
		})
	}
}

func assertSet(t *testing.T, got, want map[string]string) {
	t.Helper()
	for fn, w := range want {
		g, ok := got[fn]
		if !ok {
			t.Errorf("missing unit %q in output", fn)
			continue
		}
		if g != w {
			t.Errorf("unit %q mismatch\n--- got ---\n%s\n--- want ---\n%s", fn, g, w)
		}
	}
	for fn := range got {
		if _, ok := want[fn]; !ok {
			t.Errorf("unexpected extra unit %q\n%s", fn, got[fn])
		}
	}
}

// TestIPv6DualStack locks in the dual-stack path: a static interface with both
// v4 and v6 addresses + mixed DNS renders both Address= lines (one section, no
// per-family split) and round-trips back to the same model.
func TestIPv6DualStack(t *testing.T) {
	cfg := model.Config{
		Interfaces: []model.Interface{{
			Name:   "wan",
			Device: "eth0",
			Proto: model.StaticProto{
				IPAddr:  []netip.Prefix{netip.MustParsePrefix("192.0.2.10/24")},
				Gateway: netip.MustParseAddr("192.0.2.1"),
				IP6Addr: netip.MustParsePrefix("2001:db8::10/64"),
				IP6Gw:   netip.MustParseAddr("2001:db8::1"),
				DNS:     []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("2606:4700:4700::1111")},
			},
		}},
	}
	units, err := RenderUnits(cfg)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	wan := units["30-wan.network"]
	for _, want := range []string{
		"[Match]", "Name=eth0",
		"Address=192.0.2.10/24", "Gateway=192.0.2.1",
		"Address=2001:db8::10/64", "Gateway=2001:db8::1",
		"DNS=1.1.1.1", "DNS=2606:4700:4700::1111",
	} {
		if !strings.Contains(wan, want) {
			t.Errorf("rendered wan missing %q:\n%s", want, wan)
		}
	}

	cfg2, err := ParseUnits(units)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := cfg2.Interfaces[0].Proto.(model.StaticProto)
	want := cfg.Interfaces[0].Proto.(model.StaticProto)
	if got.Primary() != want.Primary() || got.IP6Addr != want.IP6Addr || got.Gateway != want.Gateway || got.IP6Gw != want.IP6Gw {
		t.Errorf("dual-stack round-trip mismatch:\ngot  %+v\nwant %+v", got, want)
	}
}
