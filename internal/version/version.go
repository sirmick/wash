// Package version is the single source of truth for the wash release
// version. Every binary (apps, runners, CLIs) reports this string in its
// handshake/manifest and on `--version`, instead of carrying its own
// `const version` literal — which previously drifted across ~30 files
// (some apps stuck at 0.1.0/0.9.0 while the release moved on).
//
// The value is overridable at link time so packaging can stamp the
// canonical /VERSION without editing source:
//
//	go build -ldflags "-X github.com/sirmick/wash/internal/version.Version=$(cat VERSION)" ./...
//
// The default below is the fallback for a bare `go build`/`go test` and
// must match the root VERSION file.
package version

// Version is the wash release version (see package doc). Keep in sync with
// the root VERSION file; the build stamps it from there.
var Version = "0.9.2"
