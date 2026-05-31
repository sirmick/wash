package nmprofile

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/codec"
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
