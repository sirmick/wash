//go:build wash_lite_packs

package shellassets

import "embed"

// The bundle minus the wallpapers of the packs the VM does not ship. One
// line per top-level entry of web/shell/dist; embed_test.go fails if a new
// one appears that is not listed here, so the VM never silently loses an
// asset. Wallpapers are named one by one: only Packs' own.
//
//go:embed all:assets/assets all:assets/fonts all:assets/vendor
//go:embed assets/icons.svg assets/index.html assets/shell.js assets/wash-logo.svg
//go:embed assets/wallpapers/midnight.svg assets/wallpapers/copland.svg
var FS embed.FS

// Packs is what the VM ships: Midnight (the dark default) and Copland, the
// smallest pack (~95 KB compressed for both, against ~3.4 MB for all nine).
// The ids must match web/lib/src/packs.ts, and each pack's wallpaper must
// be embedded above.
var Packs = []string{"midnight", "copland"}
