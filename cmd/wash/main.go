//go:build multicall

// cmd/wash is the multi-call wash binary. Built only with the
// `multicall` build tag; standalone (per-app) builds use the shims
// under cmd/wash-<name>/ and never touch this package.
//
// Dispatch:
//   - argv[0] = "wash-router"        → routerrun.Run
//   - argv[0] = "wash-launch"        → launchrun.Run
//   - argv[0] = "wash-<app>"         → registered app (via symlink)
//   - argv[0] = "wash" + verb        → subcommand (install-symlinks,
//     list-apps, router, launch),
//     or shorthand for wash-<app>
//
// --wash-manifest is honored on any per-app dispatch so the router's
// exec-probe path (used when selfApp's fast path doesn't match)
// returns the right manifest.
package main

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirmick/wash/internal/runner/fswatchd"
	"github.com/sirmick/wash/internal/runner/launch"
	routerrun "github.com/sirmick/wash/internal/runner/router"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/wire"
)

// vmloginRun dispatches `wash-vmlogin` when the washvmlogin build tag is set
// (see vmlogin_on.go / vmlogin_off.go). nil in the default packaging multicall.
var vmloginRun func([]string) int

func main() {
	name := filepath.Base(os.Args[0])

	// Subcommand mode: invoked as plain `wash <verb> [...]`.
	if name == "wash" {
		runSubcommand(os.Args[1:])
		return
	}

	// Special-case CLIs that aren't registered apps — they have their
	// own argv handling and don't go through the registry/--wash-manifest
	// path.
	switch name {
	case "wash-router":
		os.Exit(routerrun.Run(os.Args[1:]))
	case "wash-launch":
		os.Exit(launch.Run(os.Args[1:]))
	case "wash-vmlogin":
		// vmlogin pulls in wash-vm/guest (the in-browser/wemu VM login front),
		// which is pruned from the distro source tarball. It's compiled in only
		// with -tags=washvmlogin (the VM image build); otherwise vmloginRun is
		// nil and we fall through to the registry/not-found path.
		if vmloginRun != nil {
			os.Exit(vmloginRun(os.Args[1:]))
		}
	case "wash-fswatchd":
		// Reject the router's app-probe cleanly (exit 2, matching the
		// other non-app binaries) instead of starting the daemon, which
		// would read empty probe stdin and exit 0 with no output — the
		// router then logged a confusing "probe: no header newline". The
		// standalone cmd/wash-fswatchd shim guards this too; the multicall
		// dispatch must match.
		if len(os.Args) >= 2 && os.Args[1] == "--wash-manifest" {
			os.Exit(2)
		}
		os.Exit(fswatchd.Run(os.Args[1:]))
	}

	a := registry.Get(name)
	if a == nil {
		fmt.Fprintf(os.Stderr, "wash: no app registered for %q — was this binary built with -tags=no_app_%s?\n", name, noAppTag(name))
		os.Exit(2)
	}
	dispatchApp(a)
}

// dispatchApp runs a registered app: handles --wash-manifest (matches
// the SDK's own probe behavior so the router's exec-probe path keeps
// working when selfApp doesn't catch us) or runs the app loop.
func dispatchApp(a *registry.App) {
	if len(os.Args) >= 2 && os.Args[1] == "--wash-manifest" {
		writeManifest(os.Stdout, a)
		return
	}
	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "wash: %s: %v\n", a.Name, err)
		os.Exit(1)
	}
}

func writeManifest(w io.Writer, a *registry.App) {
	// Emit framed probe output (manifest header + raw bundle bytes) —
	// same shape the SDK's --wash-manifest path produces. The router's
	// exec-probe parser reads the header line then the raw frames.
	var bundles []wire.NamedBundle
	if a.Assets != nil {
		if b, err := readBundle(a.Assets, "index.js"); err == nil {
			bundles = append(bundles, wire.NamedBundle{Kind: wire.BundleMain, Bytes: b})
		}
		if a.Manifest.SettingsPanel != nil {
			if b, err := readBundle(a.Assets, "panel.js"); err == nil {
				bundles = append(bundles, wire.NamedBundle{Kind: wire.BundlePanel, Bytes: b})
			}
		}
	}
	if err := wire.WriteProbe(w, a.Manifest, bundles); err != nil {
		fmt.Fprintf(os.Stderr, "wash: write manifest: %v\n", err)
		os.Exit(1)
	}
}

// readBundle pulls a named bundle (index.js / panel.js) from an app's
// embedded fs.FS as raw bytes. Mirrors the SDK's path so the multicall
// and standalone probe payloads share their bundle source.
func readBundle(fsys fs.FS, name string) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

func runSubcommand(args []string) {
	if len(args) == 0 {
		usage(os.Stderr)
		os.Exit(2)
	}
	verb := args[0]
	rest := args[1:]
	switch verb {
	case "install-symlinks":
		if err := runInstallSymlinks(rest); err != nil {
			fmt.Fprintln(os.Stderr, "wash:", err)
			os.Exit(1)
		}
	case "list-apps":
		listApps(os.Stdout)
	case "router":
		os.Exit(routerrun.Run(rest))
	case "launch":
		os.Exit(launch.Run(rest))
	case "help", "-h", "--help":
		usage(os.Stdout)
	default:
		// Shorthand: `wash about` → dispatch as if exec'd via the
		// wash-about symlink. Lets you smoke-test apps without
		// running install-symlinks first.
		if a := registry.Get("wash-" + verb); a != nil {
			// Shift argv so the app sees its own name as argv[0].
			os.Args = append([]string{"wash-" + verb}, rest...)
			dispatchApp(a)
			return
		}
		fmt.Fprintf(os.Stderr, "wash: unknown verb %q\n", verb)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func listApps(w io.Writer) {
	// Stable order via sorted names. Useful for scripts that
	// shell out to `wash list-apps`.
	names := make([]string, 0, len(registry.All()))
	for n := range registry.All() {
		names = append(names, n)
	}
	// stdlib sort would import another package for two lines.
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, n := range names {
		fmt.Fprintln(w, n)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `wash — multi-call entrypoint

Symlinked usage:
  ln -s wash wash-about; ./wash-about            # run as if standalone

Subcommands:
  wash install-symlinks [<dir>]   create wash-<name> symlinks (default: dir of $0)
  wash list-apps                  list registered apps
  wash router [flags...]          run the router host (see wash router --help)
  wash launch [flags...]          run the wash-launch CLI (see wash launch --help)
  wash ai [--agent X] [dir]       open an agent session (default: first
                                  installed adapter, $HOME)
  wash <name> [args...]           shorthand for wash-<name> [args...]
  wash --wash-manifest            (not valid — manifests are per-app)`)
}

// noAppTag maps a binary name to its `no_app_<X>` opt-out tag: strip the
// leading "wash-" and replace any remaining "-" with "_" so the suggestion is
// a valid Go build tag (e.g. wash-vscode-workbench → no_app_vscode_workbench).
// Matches the tag gen-imports stamps onto cmd/wash/imports_<X>.go. Falls back
// to the full name if the input doesn't start with "wash-".
func noAppTag(s string) string {
	const p = "wash-"
	if len(s) > len(p) && s[:len(p)] == p {
		s = s[len(p):]
	}
	return strings.ReplaceAll(s, "-", "_")
}
