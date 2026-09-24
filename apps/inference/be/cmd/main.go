package main

import (
	inferenceapp "github.com/sirmick/wash/apps/inference/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() { sdk.Main(inferenceapp.Def()) }
