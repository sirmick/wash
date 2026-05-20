// Package router is wash's transport core.
//
// The router is the only spawn-capable process: it scans
// WASH_APPS_DIR at startup via the --wash-manifest probe, holds the
// browser-shell WebSocket, accepts the session app's connection over
// an inherited-fd socketpair, multiplexes frames between the two
// transports, and relays them verbatim except where it must inspect
// type for handshake, capability gating, or window lifecycle.
//
// Per WIRE.md and ARCHITECTURE.md, the router is transport, not
// interpreter — every payload on the app event channel and every raw
// channel is passed through. The router never decodes app_msg data.
package router
