// wash-session — standalone binary shim. Logic in apps/session/be.
package main

import (
	session "github.com/sirmick/wash/apps/session/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(session.Def()) }
