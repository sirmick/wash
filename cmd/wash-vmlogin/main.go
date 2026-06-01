// wash-vmlogin — standalone binary shim. Logic in internal/runner/vmlogin.
package main

import (
	"os"

	vmloginrun "github.com/sirmick/wash/internal/runner/vmlogin"
)

func main() { os.Exit(vmloginrun.Run(os.Args[1:])) }
