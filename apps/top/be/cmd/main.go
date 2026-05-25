// wash-top — standalone binary shim. Logic in apps/top/be.
package main

import (
	top "github.com/sirmick/wash/apps/top/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(top.Def()) }
