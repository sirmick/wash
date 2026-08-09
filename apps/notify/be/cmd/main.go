// wash-notify — standalone binary shim. Logic lives in apps/notify/be.
package main

import (
	notify "github.com/sirmick/wash/apps/notify/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(notify.Def()) }
