// wash-hostgw — standalone binary shim. Logic lives in apps/hostgw/be.
package main

import (
	hostgw "github.com/sirmick/wash/apps/hostgw/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(hostgw.Def()) }
