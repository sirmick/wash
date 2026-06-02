package ifupdownprofile

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sirmick/wash/internal/washnet/codec"
)

var update = flag.Bool("update", false, "rewrite golden interfaces files")

// Corpus scenarios reuse the shared .uci inputs (eth static/dhcp, bridge, vlan).
// ifupdown does no wireguard, so that scenario is omitted.
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

func uciToPkgs(t *testing.T, name string) map[string]string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join("testdata", name, "*.uci"))
	pkgs := map[string]string{}
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

// TestCompile: UCI → model → interfaces file must equal the golden.
func TestCompile(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			cfg, err := codec.Parse(uciToPkgs(t, name))
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
				t.Errorf("ifupdown render mismatch for %s:\n--- got ---\n%s\n--- want ---\n%s", name, out, want)
			}
		})
	}
}

// TestRoundTrip: Parse(golden) → model → Render reproduces the golden (text
// fixpoint; ifupdown keys by ifname, so identity is at the text layer).
func TestRoundTrip(t *testing.T) {
	for _, name := range scenarios(t) {
		t.Run(name, func(t *testing.T) {
			golden, err := os.ReadFile(goldenPath(name))
			if err != nil {
				t.Skipf("no golden (run -update): %v", err)
			}
			cfg, err := Parse(map[string]string{FileName: string(golden)})
			if err != nil {
				t.Fatalf("parse: %v", err)
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
