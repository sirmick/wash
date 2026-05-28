// wash-login — standalone binary shim. Logic in internal/runner/login.
package main

import (
	"os"

	loginrun "github.com/sirmick/wash/internal/runner/login"
)

func main() { os.Exit(loginrun.Run(os.Args[1:])) }
