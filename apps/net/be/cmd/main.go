// wash-net — standalone binary shim. Logic lives in apps/net/be.
package main

import (
	net "github.com/sirmick/wash/apps/net/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(net.Def()) }
