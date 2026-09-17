// wash-commander — standalone binary shim. Logic lives in apps/commander/be.
package main

import (
	commander "github.com/sirmick/wash/apps/commander/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(commander.Def()) }
