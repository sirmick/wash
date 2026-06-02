// Command washnet-read loads the box's current networking into the model and
// prints it as UCI — the in-guest counterpart of the UI's "load current
// settings" path (docs/NET.md §5). The backend is selected by WASH_NETD_BACKEND
// (netplan → `netplan get`; networkd → /etc/systemd/network; default nm → NM
// keyfiles), so the VM e2e can read a real netplan / networkd / NM box the same
// way netd's `current` does.
package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/sirmick/wash/apps/netd/be/ifupdown"
	"github.com/sirmick/wash/apps/netd/be/netplan"
	"github.com/sirmick/wash/apps/netd/be/networkd"
	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
)

func main() {
	var cfg model.Config
	switch os.Getenv("WASH_NETD_BACKEND") {
	case "netplan":
		cfg = netplan.NewApplier().Live()
	case "networkd":
		cfg = networkd.NewApplier().Live()
	case "ifupdown":
		cfg = ifupdown.NewApplier().Live()
	default: // nm (default, back-compat with the original keyfile reader)
		c, err := nm.ReadSystemConnections()
		if err != nil {
			fmt.Fprintln(os.Stderr, "washnet-read:", err)
			os.Exit(1)
		}
		cfg = c
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
