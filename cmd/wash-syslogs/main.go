// wash-syslogs — standalone binary shim. Logic in internal/apps/syslogs.
package main

import (
	"github.com/sirmick/wash/internal/apps/syslogs"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(syslogs.Def()) }
