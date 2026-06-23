// Command wash-fswatchd is the remote (B-side) watch daemon for wash-to-wash
// mounts: it runs inotify on the wash host and streams change events over
// stdin/stdout to the mounting host, launched `ssh <host> wash-fswatchd`. The
// logic lives in internal/runner/fswatchd so the multicall binary shares it.
package main

import (
	"os"

	"github.com/sirmick/wash/internal/runner/fswatchd"
)

func main() {
	// wash-fswatchd is a B-side mount daemon, not a desktop app. Reject the
	// router's app-probe (`<bin> --wash-manifest`, run during discovery)
	// cleanly instead of starting the daemon — reading the probe's empty
	// stdin, it otherwise exited with no manifest and the router logged a
	// confusing "probe: no header newline". Exit 2 mirrors the other non-app
	// binaries (wash-launch/login/sudo), so discovery just lists it disabled.
	for _, a := range os.Args[1:] {
		if a == "--wash-manifest" {
			os.Exit(2)
		}
	}
	os.Exit(fswatchd.Run(os.Args[1:]))
}
