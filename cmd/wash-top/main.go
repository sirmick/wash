// wash-top — standalone binary shim. Logic in internal/apps/top.
package main

import (
	"github.com/sirmick/wash/internal/apps/top"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(top.Def()) }
