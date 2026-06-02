// Command washnet-read loads the box's current networking into the model and
// prints it as UCI — the "read current situation, emit UCI" step of the CLI
// loop (read → edit → apply). The backend is WASH_NETD_BACKEND or, unset,
// autodetected (netplan/nm/networkd/ifupdown) the same way netd picks it.
package main

import (
	"fmt"
	"os"

	"github.com/sirmick/wash/apps/netd/be/backendsel"
	"github.com/sirmick/wash/cmd/internal/ucibuf"
	"github.com/sirmick/wash/internal/washnet/codec"
)

func main() {
	name := os.Getenv("WASH_NETD_BACKEND")
	if name == "" {
		name, _ = backendsel.Autodetect()
	}
	a := backendsel.New(name)
	if a == nil {
		fmt.Fprintf(os.Stderr, "washnet-read: no live backend for %q\n", name)
		os.Exit(1)
	}

	files, err := codec.Render(a.Live())
	if err != nil {
		fmt.Fprintln(os.Stderr, "washnet-read: render uci:", err)
		os.Exit(1)
	}
	fmt.Print(ucibuf.Marshal(files))
}
