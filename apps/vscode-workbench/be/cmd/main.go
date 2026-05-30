// wash-vscode-workbench — standalone shim. Logic in apps/vscode-workbench/be.
package main

import (
	vscodeworkbench "github.com/sirmick/wash/apps/vscode-workbench/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(vscodeworkbench.Def()) }
