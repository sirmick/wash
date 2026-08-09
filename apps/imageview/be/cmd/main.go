// wash-imageview — standalone binary shim. The app logic lives in
// apps/imageview/be; this imports it (init() registers + builds the
// AppDef) and hands the def to sdk.Main. The multi-call build (cmd/wash)
// imports the same package and dispatches via argv[0] instead.
package main

import (
	imageview "github.com/sirmick/wash/apps/imageview/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(imageview.Def()) }
