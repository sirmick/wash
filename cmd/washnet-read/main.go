// Command washnet-read loads the box's current NetworkManager state into the
// model and prints it as UCI — the in-guest counterpart of the UI's "load
// current settings" path (docs/NET.md §5). Reads NM's saved keyfiles and
// decompiles them through ParseKeyfiles.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/internal/washnet/codec"
)

func main() {
	cfg, err := nm.ReadSystemConnections()
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-read:", err)
		os.Exit(1)
	}
	files, err := codec.Render(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-read: render uci:", err)
		os.Exit(1)
	}
	pkgs := make([]string, 0, len(files))
	for p := range files {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		fmt.Printf("# ==== %s ====\n%s\n", p, files[p])
	}
}
