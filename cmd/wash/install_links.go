//go:build multicall

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/sirmick/wash/internal/apps/registry"
)

// runInstallSymlinks materializes wash-<app> symlinks pointing at
// this binary, in either the directory given on the command line or
// the directory containing the running wash binary by default.
//
// Idempotent: existing symlinks that already point at the right
// target are skipped. A non-symlink at the link path is left alone
// (we don't blindly clobber a real binary) and surfaced as a
// warning. Returns an error for I/O failures; a refused overwrite
// is not an error.
//
// wash-router is intentionally not symlinked here — see main.go for
// the carve-out.
func runInstallSymlinks(args []string) error {
	targetDir, err := defaultLinkDir()
	if err != nil {
		return err
	}
	if len(args) > 0 {
		targetDir = args[0]
	}
	abs, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", targetDir, err)
	}
	targetDir = abs

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve own executable: %w", err)
	}
	selfName := filepath.Base(self)

	// Names to materialize as symlinks: every registered app + the
	// two non-app CLIs that the multi-call binary also dispatches
	// (wash-router, wash-launch). wash-sudo + wash-priv-fakesudo are
	// intentionally absent — they're real separate binaries.
	names := make([]string, 0, len(registry.All())+2)
	for n := range registry.All() {
		names = append(names, n)
	}
	names = append(names, "wash-router", "wash-launch")

	var made, skipped, refused int
	for _, appName := range names {
		link := filepath.Join(targetDir, appName)
		switch action, err := installOne(link, selfName); {
		case err != nil:
			return err
		case action == "made":
			made++
			fmt.Printf("linked %s -> %s\n", link, selfName)
		case action == "skip":
			skipped++
		case action == "refuse":
			refused++
			fmt.Fprintf(os.Stderr, "refused: %s exists and is not a symlink — leaving alone\n", link)
		}
	}
	fmt.Printf("done: %d created, %d already current, %d refused\n", made, skipped, refused)
	return nil
}

// installOne handles one link path. Returns one of:
//   - "made"   — a fresh symlink was created (or replaced a stale one)
//   - "skip"   — the link already points at selfName
//   - "refuse" — the path exists as something other than a symlink
//
// Errors are returned only for actual I/O failures.
func installOne(link, selfName string) (string, error) {
	fi, err := os.Lstat(link)
	switch {
	case err == nil && fi.Mode()&os.ModeSymlink != 0:
		// Existing symlink — only replace if its target differs.
		existing, rerr := os.Readlink(link)
		if rerr == nil && existing == selfName {
			return "skip", nil
		}
		if rerr := os.Remove(link); rerr != nil {
			return "", fmt.Errorf("remove stale symlink %s: %w", link, rerr)
		}
	case err == nil:
		// Existing non-symlink — refuse to clobber.
		return "refuse", nil
	case !os.IsNotExist(err):
		return "", fmt.Errorf("lstat %s: %w", link, err)
	}
	if err := os.Symlink(selfName, link); err != nil {
		return "", fmt.Errorf("symlink %s -> %s: %w", link, selfName, err)
	}
	return "made", nil
}

// defaultLinkDir returns the directory holding the running wash
// binary, resolving symlinks so e.g. "./wash" under a symlinked
// out/ dir lands in the real out/.
func defaultLinkDir() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve own executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	return filepath.Dir(exe), nil
}
