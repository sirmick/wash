// wash-remote — standalone binary shim. Logic lives in apps/remote/be.
package main

import (
	remote "github.com/sirmick/wash/apps/remote/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(remote.Def()) }
