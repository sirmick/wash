// wash-test — standalone binary shim. Logic in apps/test/be.
package main

import (
	test "github.com/sirmick/wash/apps/test/be"
	"github.com/sirmick/wash/internal/sdk"
)

func main() { sdk.Main(test.Def()) }
