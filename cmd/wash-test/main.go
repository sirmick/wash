// wash-test — standalone binary shim. Logic in internal/apps/test.
package main

import (
	"github.com/sirmick/wash/internal/apps/test"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(test.Def()) }
