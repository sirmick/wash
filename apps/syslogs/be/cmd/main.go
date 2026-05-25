// wash-syslogs — standalone binary shim. Logic in apps/syslogs/be.
package main

import (
	syslogs "github.com/sirmick/wash/apps/syslogs/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(syslogs.Def()) }
