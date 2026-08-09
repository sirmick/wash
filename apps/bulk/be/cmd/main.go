// wash-bulk — standalone binary shim. Logic lives in apps/bulk/be.
package main

import (
	bulk "github.com/sirmick/wash/apps/bulk/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(bulk.Def()) }
