// Command washnet-read loads the box's current networking into the model and
// prints it as UCI — the "read current situation, emit UCI" step of the CLI
// loop (read → edit → apply). The backend is WASH_NETD_BACKEND or, unset,
// autodetected (netplan/nm/networkd/ifupdown) the same way netd picks it.
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/sirmick/wash/apps/netd/be/backendsel"
	"github.com/sirmick/wash/internal/washnet/codec"
	"github.com/sirmick/wash/internal/washnet/ucibuf"
)

func main() {
	// -backend wins over $WASH_NETD_BACKEND (sudo strips env, so the privileged
	// escalation passes the backend as an arg). Empty → env → autodetect.
	backendFlag := flag.String("backend", "", "backend: nm|networkd|netplan|ifupdown (default: $WASH_NETD_BACKEND or autodetect)")
	flag.Parse()

	name, src := *backendFlag, "flag"
	if name == "" {
		name, src = os.Getenv("WASH_NETD_BACKEND"), "env"
	}
	if name == "" {
		var why string
		name, why = backendsel.Autodetect()
		src = "autodetect: " + why
	}
	fmt.Fprintf(os.Stderr, "washnet-read: backend=%s (%s)\n", name, src)
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
