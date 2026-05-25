// wash-edit — standalone binary shim. Logic in apps/edit/be.
package main

import (
	edit "github.com/sirmick/wash/apps/edit/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(edit.Def()) }
