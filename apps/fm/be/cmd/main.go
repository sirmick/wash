// wash-fm — standalone binary shim. Logic in apps/fm/be.
package main

import (
	fm "github.com/sirmick/wash/apps/fm/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(fm.Def()) }
