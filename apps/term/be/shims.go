// PATH shims for a wash terminal.
//
// docs/Review-findings.md, cross-app: "`wash open <path>`, `xdg-open`,
// `$EDITOR`, `$BROWSER` from a wash terminal". `wash open` is the verb
// (internal/runner/launch/open.go); this is what makes the programs that
// already know how to open things — a pager's `o`, `gh browse`, git's
// web--browse, half of npm — reach it without being taught wash exists.
//
// The shim is a SYMLINK to the running binary named `xdg-open`, not a
// script: the multicall entrypoint dispatches on argv[0], so `xdg-open a
// b.png` arrives as `wash open` with exactly the arguments it was given.
// A shell wrapper would have to re-quote them, and would get a filename
// with a newline in it wrong.
//
// It lives in a per-user runtime directory rather than $WASH_BIN_DIR: the
// packaged bin dir is /usr/bin, which is not ours to write to, and a shim
// that only appears in a dev tree is worse than none.
//
// $EDITOR and $VISUAL are deliberately NOT set. Both are contracts to
// BLOCK until the file is closed — git commit, crontab -e and visudo all
// depend on it, and each would corrupt or silently discard the user's
// edit if the editor returned immediately. wash-edit has no such mode
// (`wash edit-wait` would need the app to report the file's close back
// over the wire and the CLI to hold the terminal until it did), so the
// honest answer is to leave the user's own $EDITOR alone.
package term

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
)

// shimNames are the argv[0]s cmd/wash answers to that belong on a
// terminal's PATH.
var shimNames = []string{"xdg-open"}

// installShims materialises the shim directory and returns its path, or
// "" if it could not be made. Never fatal: a terminal without shims is
// the terminal wash shipped yesterday.
//
// The directory is per-user and per-binary (wash-shims-<uid>-<hash of
// the binary's path>), so two sessions running different builds — the
// packaged /usr/bin one and a dev tree, or parallel e2e routers — never
// repoint each other's xdg-open, and it is rebuilt on every start rather
// than trusted. Siblings whose binary is gone (an e2e run's apps dir, an
// old dev tree) are removed on the way.
func installShims() string {
	self, err := os.Executable()
	if err != nil {
		log.Printf("wash-term: shims: %v (skipped)", err)
		return ""
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	prefix := fmt.Sprintf("wash-shims-%d-", os.Getuid())
	sum := sha256.Sum256([]byte(self))
	dir := filepath.Join(base, prefix+hex.EncodeToString(sum[:6]))
	pruneShims(base, prefix, dir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		log.Printf("wash-term: shims %s: %v (skipped)", dir, err)
		return ""
	}
	made := 0
	for _, name := range shimNames {
		link := filepath.Join(dir, name)
		if cur, err := os.Readlink(link); err == nil && cur == self {
			made++
			continue
		}
		// Replace rather than reuse: after an upgrade the old target is
		// a binary that may no longer exist.
		_ = os.Remove(link)
		if err := os.Symlink(self, link); err != nil {
			log.Printf("wash-term: shim %s: %v (skipped)", link, err)
			continue
		}
		made++
	}
	if made == 0 {
		return ""
	}
	log.Printf("wash-term: shims dir=%s names=%v", dir, shimNames)
	return dir
}

// pruneShims removes this user's other shim directories whose links lead
// nowhere: the binary they named was deleted, so no terminal can use them.
// A directory whose binary still exists belongs to a live (or reusable)
// build and is left alone.
func pruneShims(base, prefix, keep string) {
	entries, err := os.ReadDir(base)
	if err != nil {
		return
	}
	for _, e := range entries {
		dir := filepath.Join(base, e.Name())
		if !e.IsDir() || !strings.HasPrefix(e.Name(), prefix) || dir == keep {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, shimNames[0]))
		if err != nil {
			continue
		}
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			_ = os.RemoveAll(dir)
		}
	}
}
