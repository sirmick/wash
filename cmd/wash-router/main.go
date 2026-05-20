// wash-router — the wash router and browser shell host.
//
// v0.0 stub: prints identity and exits. Real behavior lands in commit 3.
package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-stub"

func main() {
	fmt.Fprintf(os.Stdout, "wash-router %s (stub)\n", version)
}
