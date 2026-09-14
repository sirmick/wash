package main

import (
	ai "github.com/sirmick/wash/apps/ai/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(ai.AgentsDef()) }
