// wash-about — standalone binary shim. The app logic lives in
// apps/about/be; this file just imports it (init() registers
// + builds the AppDef) and hands the def to sdk.Main. The multi-call
// build (cmd/wash) imports the same package and dispatches via
// argv[0] instead.
package main

import (
	about "github.com/sirmick/wash/apps/about/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(about.Def()) }
