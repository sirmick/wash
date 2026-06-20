// wash-fswatch — standalone binary shim. Logic lives in apps/fswatch/be.
package main

import (
	fswatchsvc "github.com/sirmick/wash/apps/fswatch/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(fswatchsvc.Def()) }
