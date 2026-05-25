// wash-priv — standalone binary shim. Logic in apps/priv/be.
package main

import (
	priv "github.com/sirmick/wash/apps/priv/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(priv.Def()) }
