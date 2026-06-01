//go:build multicall && !no_app_netd

package main

// Register the wash-netd background service (com.wash.netd) into the multicall
// binary. docs/NET.md §2.11.
import _ "github.com/sirmick/wash/apps/netd/be"
