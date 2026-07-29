// wash-agent-hook — the standalone shim for the agent hook helper.
//
// FE-less CLI, so it follows the wash-fswatchd shape: all the logic lives
// in internal/agenthook, this file only exists so the binary can be built
// on its own (the shipped layout dispatches it out of the multicall
// binary by argv[0]).
//
// See docs/AGENT_TERM.md §4.
package main

import (
	"os"

	"github.com/sirmick/wash/internal/agenthook"
)

func main() {
	// The router probes every wash-* binary in its apps dir for a
	// manifest. This is a CLI, not an app: answer the probe the way the
	// other non-app binaries do (exit 2, no output) instead of reading
	// the probe's empty stdin as a hook payload.
	if len(os.Args) >= 2 && os.Args[1] == "--wash-manifest" {
		os.Exit(2)
	}
	os.Exit(agenthook.Run(os.Args[1:]))
}
