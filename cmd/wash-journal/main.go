// wash-journal — standalone binary shim. Logic in internal/apps/journal.
package main

import (
	"github.com/sirmick/wash/internal/apps/journal"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(journal.Def()) }
