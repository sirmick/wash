// wash-edit — standalone binary shim. Logic in internal/apps/edit.
package main

import (
	"github.com/sirmick/wash/internal/apps/edit"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(edit.Def()) }
