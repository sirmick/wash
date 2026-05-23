// wash-priv — standalone binary shim. Logic in internal/apps/priv.
package main

import (
	"github.com/sirmick/wash/internal/apps/priv"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(priv.Def()) }
