// Package shellassets holds the built wash desktop shell runtime
// (index.html, shell.js, /vendor, icons, fonts, wallpapers) embedded
// once and shared by every binary that serves it:
//
//   - wash-router serves it over HTTP (router/http.go) and, transport-
//     agnostically, over the asset.read wire channel (shell_session.go),
//     so the serial/virtio-console transports work with no HTTP listener.
//   - wash-login serves it over its post-auth HTTP root — the only HTTP
//     origin in multi-user deployments, where per-user routers are
//     Unix-socket only.
//
// A single //go:embed here replaces the former per-binary embeds plus the
// Makefile copy-stage that mirrored the router's bundle into wash-login.
// The ./assets tree is a gitignored build artifact: the Makefile stages
// web/shell/dist into it ($(SHELL_STAMP)) before the go build.
package shellassets

import "embed"

// FS is the embedded shell-runtime tree, rooted such that the bundle
// lives under "assets/" (e.g. "assets/index.html", "assets/shell.js",
// "assets/vendor/…"). Consumers fs.Sub it to "assets". Empty in
// router-only test builds where the embed stamp wasn't produced.
//
//go:embed all:assets
var FS embed.FS
