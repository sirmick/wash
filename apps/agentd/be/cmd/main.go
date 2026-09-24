// wash-agentd — standalone binary shim. Logic lives in apps/agentd/be.
package main

import (
	"os"

	agentd "github.com/sirmick/wash/apps/agentd/be"
	"github.com/sirmick/wash/internal/workspacemcp"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == workspacemcp.Argument {
		os.Exit(workspacemcp.Run())
	}
	sdk.Main(agentd.Def())
}
