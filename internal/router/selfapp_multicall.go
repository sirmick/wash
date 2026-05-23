//go:build multicall

package router

import (
	"os"
	"path/filepath"

	"github.com/sirmick/wash/internal/apps/registry"
)

// selfApp returns the in-process app registration when bin resolves
// to the router's own executable, or nil for external binaries and
// names the build does not include.
//
// Compiled in only by `go build -tags=multicall`. Lets the catalog
// scan skip exec-probing every wash-<name> symlink that's actually
// just a link back to this binary — the manifest is already in
// memory via the apps/registry init() chain. Trust + auth gates
// still apply; this only short-circuits parse + fork.
//
// EvalSymlinks is used on both sides so the comparison happens at
// the resolved-path level (handles arbitrary chains of symlinks).
// Hardlinks defeat the optimization (different paths, same inode);
// that falls through to exec-probe and still works, just slower.
func selfApp(bin string) *registry.App {
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	selfR, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil
	}
	binR, err := filepath.EvalSymlinks(bin)
	if err != nil {
		return nil
	}
	if selfR != binR {
		return nil
	}
	return registry.Get(filepath.Base(bin))
}
