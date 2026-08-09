// wash-disks — standalone binary shim. Logic in apps/disks/be.
package main

import (
	"context"
	"fmt"
	"os"

	disks "github.com/sirmick/wash/apps/disks/be"
	"github.com/sirmick/wash/pkg/sdk"
)

func main() {
	// --dump-snapshot: one-shot router-free JSON dump (VM tests / debugging).
	for _, a := range os.Args[1:] {
		if a == "--dump-snapshot" {
			if err := disks.DumpSnapshot(context.Background(), os.Stdout); err != nil {
				fmt.Fprintf(os.Stderr, "wash-disks: %v\n", err)
				os.Exit(1)
			}
			return
		}
	}
	sdk.Main(disks.Def())
}
