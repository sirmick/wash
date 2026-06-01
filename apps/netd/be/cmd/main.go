// wash-netd — standalone binary shim. Logic lives in apps/netd/be.
package main

import (
	netd "github.com/sirmick/wash/apps/netd/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(netd.Def()) }
