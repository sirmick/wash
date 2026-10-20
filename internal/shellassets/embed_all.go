//go:build !wash_lite_packs

package shellassets

import "embed"

//go:embed all:assets
var FS embed.FS

// Packs is nil: every pack's wallpaper is embedded.
var Packs []string
