package main

import (
	sessionsummary "github.com/sirmick/wash/apps/session-summary/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(sessionsummary.Def()) }
