// wash-radio — standalone binary shim. Logic lives in apps/radio/be.
package main

import (
	radio "github.com/sirmick/wash/apps/radio/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(radio.Def()) }
