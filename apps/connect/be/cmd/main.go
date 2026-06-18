// wash-connect — standalone binary shim. The app logic lives in
// apps/connect/be; this file imports it (init() registers + builds the
// AppDef) and hands the def to sdk.Main. The multi-call build (cmd/wash)
// imports the same package and dispatches via argv[0] instead.
package main

import (
	connect "github.com/sirmick/wash/apps/connect/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(connect.Def()) }
