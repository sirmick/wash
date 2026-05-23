// wash-settings — standalone binary shim. Logic in internal/apps/settings.
package main

import (
	"github.com/sirmick/wash/internal/apps/settings"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(settings.Def()) }
