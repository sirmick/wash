// wash-about — standalone binary shim. The app logic lives in
// internal/apps/about; this file just imports it (init() registers
// + builds the AppDef) and hands the def to sdk.Main. The multi-call
// build (cmd/wash) imports the same package and dispatches via
// argv[0] instead.
package main

import (
	"github.com/sirmick/wash/internal/apps/about"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(about.Def()) }
