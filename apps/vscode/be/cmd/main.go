// wash-vscode — standalone binary shim. Logic in apps/vscode/be.
package main

import (
	vscode "github.com/sirmick/wash/apps/vscode/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(vscode.Def()) }
