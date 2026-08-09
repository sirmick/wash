// wash-term — standalone binary shim. Logic in apps/term/be.
package main

import (
	term "github.com/sirmick/wash/apps/term/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(term.Def()) }
