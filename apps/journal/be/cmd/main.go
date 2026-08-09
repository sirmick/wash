// wash-journal — standalone binary shim. Logic in apps/journal/be.
package main

import (
	journal "github.com/sirmick/wash/apps/journal/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(journal.Def()) }
