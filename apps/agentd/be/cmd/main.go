// wash-agentd — standalone binary shim. Logic lives in apps/agentd/be.
package main

import (
	agentd "github.com/sirmick/wash/apps/agentd/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(agentd.Def()) }
