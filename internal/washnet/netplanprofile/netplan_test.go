package netplanprofile

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/codec"
)

var update = flag.Bool("update", false, "rewrite .yaml golden files")

// Each corpus scenario is a dir under testdata/ holding the canonical UCI source
// (one <package>.uci per package — the same backend-agnostic input the
// nmprofile/networkdprofile corpora use) and the rendered netplan golden
// (00-wash.yaml). netplan covers the IP layer: eth, bridge, vlan, wireguard.
func scenarios(t *testing.T) []string {
	t.Helper()
	dirs, _ := filepath.Glob("testdata/*")
	var out []string
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			out = append(out, filepath.Base(d))
		}
	}
	sort.Strings(out)
	return out
}

// uciToConfig reads a scenario's *.uci into a model.Config.
func uciToConfig(t *testing.T, name string) (pkgs map[string]string) {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", name, "*.uci"))
	pkgs = map[string]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		pkgs[strings.TrimSuffix(filepath.Base(f), ".uci")] = string(b)
	}
	return pkgs
}

func goldenPath(name string) string { return filepath.Join("testdata", name, FileName) }

// TestCompile: canonical UCI → model → netplan YAML must equal the golden.
func TestCompile(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			cfg, err := codec.Parse(uciToConfig(t, name))
			if err != nil {
				t.Fatalf("parse uci: %v", err)
			}
			got, err := Render(cfg)
			if err != nil {
				t.Fatalf("render: %v", err)
			}
			out := got[FileName]
			if *update {
				if err := os.WriteFile(goldenPath(name), []byte(out), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Fatalf("read golden (run -update?): %v", err)
			}
			if out != string(want) {
				t.Errorf("netplan render mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", name, out, want)
			}
		})
	}
}

// TestRoundTrip: Parse(golden) → model → Render must reproduce the golden
// (YAML-level fixpoint). netplan keys by device, so the UCI interface name isn't
// preserved — identity is at the YAML layer, not the model layer.
func TestRoundTrip(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			golden, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Skipf("no golden (run -update): %v", err)
			}
			cfg, err := Parse(map[string]string{FileName: string(golden)})
			if err != nil {
				t.Fatalf("parse netplan: %v", err)
			}
			got, err := Render(cfg)
			if err != nil {
				t.Fatalf("re-render: %v", err)
			}
			if got[FileName] != string(golden) {
				t.Errorf("round-trip not a fixpoint for %s:\n--- re-rendered ---\n%s\n--- golden ---\n%s", name, got[FileName], golden)
			}
		})
	}
}
