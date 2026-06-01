// Command washnet-apply renders the given UCI config and applies it to the live
// box. The in-guest apply tool: run it over the ctl plane to configure the VM's
// NICs, then assert the result with ip/networkctl. Backend selected by
// WASH_NETD_BACKEND (networkd → systemd-networkd units + networkctl; otherwise
// NetworkManager keyfiles + D-Bus), mirroring how netd picks its Applier.
//
//	washnet-apply network.uci [wireless.uci ...]   # keyed by package = filename stem
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/apps/netd/be/networkd"
	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/internal/washnet/backend"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/model"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: washnet-apply <pkg.uci> [pkg.uci ...]")
		os.Exit(2)
	}
	pkgs := map[string]string{}
	for _, p := range os.Args[1:] {
		b, err := os.ReadFile(p)
		if err != nil {
			fmt.Fprintln(os.Stderr, "washnet-apply:", err)
			os.Exit(1)
		}
		pkgs[strings.TrimSuffix(filepath.Base(p), filepath.Ext(p))] = string(b)
	}
	cfg, err := codec.Parse(pkgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-apply: parse uci:", err)
		os.Exit(1)
	}

	if os.Getenv("WASH_NETD_BACKEND") == "networkd" {
		applyNetworkd(cfg)
		return
	}
	applyNM(cfg)
}

// applyNetworkd renders to systemd-networkd units and applies them through the
// real Applier (write dir + networkctl reload/reconfigure), then Confirm so the
// units persist (no auto-revert for the CLI path).
func applyNetworkd(cfg model.Config) {
	a := networkd.NewApplier()
	token, err := a.Apply(backend.RenderPlan{Target: cfg})
	if err != nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: APPLY FAILED: %v\n", err)
		os.Exit(1)
	}
	if err := a.Confirm(token); err != nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: confirm: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("APPLIED networkd units")
}

func applyNM(cfg model.Config) {
	c, err := nm.Connect()
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-apply: connect NM:", err)
		os.Exit(1)
	}
	defer c.Close()

	ids, err := c.Apply(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "washnet-apply: APPLY FAILED after %v: %v\n", ids, err)
		os.Exit(1)
	}
	fmt.Printf("APPLIED %d connections: %s\n", len(ids), strings.Join(ids, " "))
}
