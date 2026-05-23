// wash-router — standalone binary shim. Logic in internal/runner/router.
package main

import (
	"os"

	routerrun "github.com/sirmick/wash/internal/runner/router"
)

func main() { os.Exit(routerrun.Run(os.Args[1:])) }
