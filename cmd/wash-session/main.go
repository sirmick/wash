// wash-session — standalone binary shim. Logic in internal/apps/session.
package main

import (
	"github.com/sirmick/wash/internal/apps/session"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(session.Def()) }
