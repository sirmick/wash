// wash-launch — standalone binary shim. Logic in internal/runner/launch.
package main

import (
	"os"

	"github.com/sirmick/wash/internal/runner/launch"
)

func main() { os.Exit(launch.Run(os.Args[1:])) }
