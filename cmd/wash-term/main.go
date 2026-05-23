// wash-term — standalone binary shim. Logic in internal/apps/term.
package main

import (
	"github.com/sirmick/wash/internal/apps/term"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(term.Def()) }
