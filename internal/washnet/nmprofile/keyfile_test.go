package nmprofile

import (
	"flag"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/sirmick/wash/internal/washnet/codec"
)

var update = flag.Bool("update", false, "rewrite .nmconnection golden files")

// TestRenderKeyfileGoldens is the pure "compile to NM profiles" gate: each
// sample, authored as canonical UCI, parses to the model and renders to a
// golden NM keyfile — no D-Bus, no VM (docs/NET.md §9). Run with -update to
// regenerate goldens after an intentional mapping change, then eyeball them.
func TestRenderKeyfileGoldens(t *testing.T) {
	// sample → the connection id we assert on.
	cases := map[string]string{
		"eth-static": "eth-static",
		"eth-dhcp":   "eth-dhcp",
	}
	names := make([]string, 0, len(cases))
	for n := range cases {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		conn := cases[name]
		t.Run(name, func(t *testing.T) {
			uci, err := os.ReadFile(filepath.Join("testdata", name+".uci"))
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := codec.Parse(map[string]string{"network": string(uci)})
			if err != nil {
				t.Fatalf("parse uci: %v", err)
			}
			kfs, err := RenderKeyfiles(cfg)
			if err != nil {
				t.Fatalf("render keyfiles: %v", err)
			}
			got, ok := kfs[conn]
			if !ok {
				have := make([]string, 0, len(kfs))
				for k := range kfs {
					have = append(have, k)
				}
				t.Fatalf("no connection %q rendered; got %v", conn, have)
			}

			golden := filepath.Join("testdata", name+".nmconnection")
			if *update {
				if err := os.WriteFile(golden, []byte(got), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("updated %s", golden)
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("read golden (run -update first?): %v", err)
			}
			if got != string(want) {
				t.Fatalf("keyfile mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
			}
		})
	}
}
