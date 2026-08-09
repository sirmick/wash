// wash-music — standalone binary shim. Logic lives in apps/music/be.
package main

import (
	music "github.com/sirmick/wash/apps/music/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(music.Def()) }
