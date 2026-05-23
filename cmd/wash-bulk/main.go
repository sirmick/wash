// wash-bulk — standalone binary shim. Logic lives in internal/apps/bulk.
package main

import (
	"github.com/sirmick/wash/internal/apps/bulk"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(bulk.Def()) }
