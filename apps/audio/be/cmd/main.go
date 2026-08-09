// wash-audio — standalone binary shim. Logic lives in apps/audio/be.
package main

import (
	audio "github.com/sirmick/wash/apps/audio/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(audio.Def()) }
