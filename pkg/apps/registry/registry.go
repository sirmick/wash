// Package registry is the in-process catalog of wash apps compiled
// into this binary.
//
// Two consumers populate it:
//   - A standalone shim (cmd/wash-<name>/main.go) imports the single
//     internal/apps/<name> package, whose init() calls Register once.
//     registry.All() then has exactly one entry — the shim's own app.
//   - The multi-call entrypoint (cmd/wash, built with -tags=multicall)
//     blank-imports every enabled internal/apps/<name>; every init()
//     fires; registry.All() ends up holding the full set.
//
// The router consults registry.Get via internal/router/selfapp.go to
// short-circuit exec-probe for symlinks that resolve to itself — the
// compiled-in manifest is the same struct ParseManifest would have
// produced from --wash-manifest output, so the fast path is purely
// a parse + fork savings.
package registry

import (
	"context"
	"fmt"
	"io/fs"

	"github.com/sirmick/wash/pkg/wire"
)

// App is one compiled-in wash app. The fields mirror what a registry
// Entry needs: a name (used as the symlink/binary basename in the
// catalog), the canonical wire manifest, the embedded asset FS, and
// the entrypoint that runs the app event loop.
type App struct {
	Name     string                      // "wash-term", matches argv[0]
	Manifest wire.Manifest               // same struct ParseManifest produces
	Assets   fs.FS                       // //go:embed all:assets
	Run      func(context.Context) error // app event loop
}

var apps = map[string]*App{}

// Register adds a to the registry. Called from each app package's
// init(). Manifest validation runs here so any drift between source
// and the runtime contract surfaces at startup, not silently in the
// catalog. Duplicate Name registrations panic — they're a build-time
// configuration error (two apps claiming the same binary name).
func Register(a *App) {
	if a == nil {
		panic("registry.Register: nil App")
	}
	if a.Name == "" {
		panic("registry.Register: empty Name")
	}
	if a.Run == nil {
		panic(fmt.Sprintf("registry.Register: %s has nil Run", a.Name))
	}
	if err := wire.ValidateManifest(&a.Manifest); err != nil {
		panic(fmt.Sprintf("registry.Register: %s invalid manifest: %v", a.Name, err))
	}
	if _, dup := apps[a.Name]; dup {
		panic(fmt.Sprintf("registry.Register: duplicate app %q", a.Name))
	}
	apps[a.Name] = a
}

// Get returns the registered app by name, or nil. The router's
// self-aware fast path uses this; the standalone shim uses it to
// find its single registered app.
func Get(name string) *App { return apps[name] }

// All returns a snapshot of every registered app. The multi-call
// install-symlinks subcommand iterates this to materialize symlinks.
func All() map[string]*App {
	out := make(map[string]*App, len(apps))
	for k, v := range apps {
		out[k] = v
	}
	return out
}

// reset clears the registry. Test-only.
func reset() { apps = map[string]*App{} }
