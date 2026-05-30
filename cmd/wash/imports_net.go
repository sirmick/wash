//go:build multicall && !no_app_net

package main

// Register the windowed wash-net app (com.wash.net) into the multicall binary.
// docs/NET.md §2.11.
import _ "github.com/sirmick/wash/apps/net/be"
