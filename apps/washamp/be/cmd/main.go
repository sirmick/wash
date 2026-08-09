// wash-washamp — standalone binary shim. Logic lives in apps/washamp/be.
package main

import (
	washamp "github.com/sirmick/wash/apps/washamp/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(washamp.Def()) }
