//go:build multicall

package main

import (
	"os"
	"regexp"
	"testing"

	"github.com/sirmick/wash/pkg/apps/registry"
)

// spritePath is the icon sprite source, relative to this package dir
// (cmd/wash). build-icons.mjs lists every lucide symbol baked into the shell
// sprite (icons.svg); a manifest Icon not in it renders blank at runtime.
const spritePath = "../../web/shell/build-icons.mjs"

// spriteIconRe matches one ICONS entry — a single-quoted lucide name at the
// start of a line (optionally indented). Anchored to line start so it ignores
// quoted strings mid-line (e.g. the require.resolve path).
var spriteIconRe = regexp.MustCompile(`(?m)^\s*'([a-z0-9-]+)'`)

// loadSprite parses the lucide symbol names from build-icons.mjs.
func loadSprite(t *testing.T) map[string]bool {
	t.Helper()
	src, err := os.ReadFile(spritePath)
	if err != nil {
		t.Fatalf("read sprite source %s: %v", spritePath, err)
	}
	set := map[string]bool{}
	for _, m := range spriteIconRe.FindAllStringSubmatch(string(src), -1) {
		set[m[1]] = true
	}
	if len(set) == 0 {
		t.Fatalf("parsed 0 icons from %s — regex or file format changed", spritePath)
	}
	return set
}

// TestManifestIconsInSprite asserts every icon a registered app's manifest
// references (the app icon, its settings-panel nav icon, and its root-variant
// icon) is present in the shell sprite. Catches the silent "missing icon"
// failure mode at build time — a new app whose Icon isn't in build-icons.mjs.
// Runs under -tags=multicall (the registry is populated by the imports_*.go
// blank-imports); e2e-test already runs `go test -tags=multicall ./cmd/wash/...`.
func TestManifestIconsInSprite(t *testing.T) {
	sprite := loadSprite(t)

	check := func(t *testing.T, app, field, icon string) {
		if icon == "" {
			return
		}
		if !sprite[icon] {
			t.Errorf("%s: manifest %s icon %q is not in the shell sprite (%s) — add it to the ICONS array",
				app, field, icon, spritePath)
		}
	}

	apps := registry.All()
	if len(apps) == 0 {
		t.Fatal("no apps registered — build tag multicall missing?")
	}
	for name, app := range apps {
		check(t, name, "icon", app.Manifest.Icon)
		if sp := app.Manifest.SettingsPanel; sp != nil {
			check(t, name, "settings_panel.icon", sp.Icon)
		}
		// RootVariant.Icon is an optional override; when empty the launcher
		// falls back to the app's own Manifest.Icon (session rootEntryFor:
		// `src.root_variant.icon ?? src.icon`), already checked above.
		if rv := app.Manifest.RootVariant; rv != nil {
			check(t, name, "root_variant.icon", rv.Icon)
		}
	}
}
