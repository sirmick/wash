// wash-session — the session app (declares surface=desktop).
//
// v0.0 stub: prints identity and exits. Real behavior lands in commit 6.
package main

import (
	"fmt"
	"os"
)

const version = "0.0.0-stub"

func main() {
	fmt.Fprintf(os.Stdout, "wash-session %s (stub)\n", version)
}
