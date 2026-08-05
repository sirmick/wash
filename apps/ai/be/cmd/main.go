// wash-ai — standalone binary shim. The app logic lives in apps/ai/be;
// this file imports it (init() registers + builds the AppDef) and hands
// the def to sdk.Main. The multi-call build (cmd/wash) imports the same
// package and dispatches via argv[0] instead.
package main

import (
	ai "github.com/sirmick/wash/apps/ai/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(ai.Def()) }
