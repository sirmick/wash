// wash-about — the About app (declares surface=window).
//
// v0.0 stub: prints identity and exits. Real behavior lands in commit 7.
package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-stub"

func main() {
	fmt.Fprintf(os.Stdout, "wash-about %s (stub)\n", version)
}
