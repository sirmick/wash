// wash-journal — standalone binary shim. Logic in apps/journal/be.
package main

import (
	journal "github.com/sirmick/wash/apps/journal/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(journal.Def()) }
