// wash-settings — standalone binary shim. Logic in apps/settings/be.
package main

import (
	settings "github.com/sirmick/wash/apps/settings/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(settings.Def()) }
