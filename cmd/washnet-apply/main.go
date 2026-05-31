// Command washnet-apply renders the given UCI config to NM keyfiles and applies
// it to the live NetworkManager (write system-connections + reload + activate).
// The B4c in-guest apply tool: run it over the ctl plane to configure the VM's
// NICs, then assert the result with ip/nmcli.
//
//	washnet-apply network.uci [wireless.uci ...]   # keyed by package = filename stem
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/apps/netd/be/nm"
	"github.com/sirmick/wash/internal/washnet/codec"
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
