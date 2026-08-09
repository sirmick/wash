// wash-packages — standalone binary shim. Logic in apps/packages/be.
package main

import (
	packages "github.com/sirmick/wash/apps/packages/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(packages.Def()) }
