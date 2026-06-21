// Command wash-fswatchd is the remote (B-side) watch daemon for wash-to-wash
// mounts: it runs inotify on the wash host and streams change events over
// stdin/stdout to the mounting host, launched `ssh <host> wash-fswatchd`. The
// logic lives in internal/runner/fswatchd so the multicall binary shares it.
package main

import (
	"os"

	"github.com/sirmick/wash/internal/runner/fswatchd"
)

func main() { os.Exit(fswatchd.Run(os.Args[1:])) }
