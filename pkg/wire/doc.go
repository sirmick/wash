// Package wire is wash's framing and message vocabulary.
//
// It implements the wire protocol from docs/WIRE.md: the
// [flags|channel|length|payload] frame format, a small per-channel
// dispatcher (Mux), and JSON message types for both the control
// channels (handshake, asset pull, browser↔router shell vocabulary,
// errors) and the app event channel.
//
// The router uses this package to demultiplex frames it relays
// verbatim; the SDK uses it to parse and emit messages on the channels
// it owns. Higher-level semantics (spawn, supervision, window
// lifecycle decisions) live in router and sdk, not here.
package wire
