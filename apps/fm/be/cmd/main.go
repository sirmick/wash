// wash-fm — standalone binary shim. Logic in apps/fm/be.
package main

import (
	"os"

	fm "github.com/sirmick/wash/apps/fm/be"
	"github.com/sirmick/wash/internal/runner/launch"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() {
	def := fm.Def()
	// Typed at a wash terminal's prompt, not spawned by the router: the
	// terminal opens it (cmd/wash's dispatchApp does the same).
	if launch.InTerminalShell(def.Manifest.ID) {
		os.Exit(launch.RunOpenIn(def.Manifest.ID, os.Args[1:]))
	}
	sdk.Main(def)
}
