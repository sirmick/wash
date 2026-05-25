// wash-services — standalone binary shim. Logic in apps/services/be.
package main

import (
	services "github.com/sirmick/wash/apps/services/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(services.Def()) }
