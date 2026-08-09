// Package sdk is wash's app-side binding.
//
// An app's main() calls sdk.Main(def). If the process was invoked
// with --wash-manifest, the SDK prints the manifest JSON to stdout
// and exits 0 (per WIRE.md §5) — the app's own code never runs.
// Otherwise the SDK adopts fd 3 as the inherited Unix socket, runs
// the WIRE.md §6 handshake, and dispatches events to the AppDef
// callbacks until the connection closes (Tier 2 from the SDK
// design).
//
// The SDK serves the app's bundled FE assets from a caller-provided
// fs.FS in response to asset.read frames (WIRE.md §7). The router
// relays those bytes onward to the browser shell.
package sdk
