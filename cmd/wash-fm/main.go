// wash-fm — standalone binary shim. Logic in internal/apps/fm.
package main

import (
	"github.com/sirmick/wash/internal/apps/fm"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(fm.Def()) }
