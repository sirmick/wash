// Extra roots for a session (docs/Review-findings.md P2 → agent: "fs and
// terminal confined to the session cwd with no override — monorepo
// sibling dirs unreadable").
//
// The session cwd is the scope the user consented to when they started
// the agent, and it is the right default. It is the wrong LIMIT: a change
// in one package of a monorepo routinely needs to read a sibling, a
// generated schema lives in a different tree, and today the only way to
// give an agent both was to start it at a parent directory — which grants
// far more than the two folders it needed.
//
// So a session carries a set of roots: the cwd, plus whatever the person
// added. Both confinements read the same set — the fs capability
// (acpfs.go) and the terminal's cwd (acpterm.go) — because a folder an
// agent may read and may not run anything in is a distinction nobody
// asked for and one nobody would maintain.
//
// A path outside every root is no longer a silent refusal. It raises the
// same permission question a tool call does: the agent said what it
// wants, the person can say yes, and a "no" is at least visible. A silent
// refusal read to the person as the agent being broken, and to the agent
// as the file not existing.
package agentd

import (
	"context"
	"log"
	"path/filepath"
	"sort"

	wfs "github.com/sirmick/wash/internal/fs"
)

// maxExtraRoots bounds what one session can accumulate. A session with
// twenty roots has no confinement worth the name; the number is a
// reminder that each one is a decision.
const maxExtraRoots = 8

// roots returns every folder this session may reach: the cwd first, then
// the extras in the order they were added.
func (h *hosted) roots() []string {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	out := make([]string, 0, 1+len(h.extraRoots))
	out = append(out, h.cwd)
	return append(out, h.extraRoots...)
}

// extraRootsSnapshot is what the roster publishes: the added folders only,
// because the cwd is already a column.
func (h *hosted) extraRootsSnapshot() []string {
	hostedMu.Lock()
	defer hostedMu.Unlock()
	if len(h.extraRoots) == 0 {
		return nil
	}
	return append([]string(nil), h.extraRoots...)
}

// addRoot allows another folder. Returns false when the path is unusable,
// already covered, or the session is full. Idempotent: adding a folder
// twice is a no-op rather than a duplicate row in the status bar.
func (h *hosted) addRoot(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil || abs == "" || abs == "/" {
		// "/" is not an extra root, it is the absence of confinement, and
		// a person choosing it in a file picker has almost certainly not
		// meant it.
		log.Printf("agentd: root refused key=%s path=%q", h.key, path)
		return false
	}
	abs = filepath.Clean(abs)
	hostedMu.Lock()
	defer hostedMu.Unlock()
	if len(h.extraRoots) >= maxExtraRoots {
		log.Printf("agentd: root refused key=%s path=%s: already at %d", h.key, abs, maxExtraRoots)
		return false
	}
	for _, r := range append([]string{h.cwd}, h.extraRoots...) {
		if within(r, abs) {
			return false
		}
	}
	h.extraRoots = append(h.extraRoots, abs)
	log.Printf("agentd: root added key=%s path=%s roots=%d", h.key, abs, 1+len(h.extraRoots))
	return true
}

// removeRoot takes a folder back. The cwd cannot be removed: a session
// with no root is a session that can do nothing, and "end it" is the verb
// for that.
func (h *hosted) removeRoot(path string) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	hostedMu.Lock()
	defer hostedMu.Unlock()
	for i, r := range h.extraRoots {
		if r == abs {
			h.extraRoots = append(h.extraRoots[:i:i], h.extraRoots[i+1:]...)
			log.Printf("agentd: root removed key=%s path=%s roots=%d", h.key, abs, 1+len(h.extraRoots))
			return true
		}
	}
	return false
}

// within reports whether abs is root or lives under it.
func within(root, abs string) bool {
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !hasDotDotPrefix(rel))
}

func hasDotDotPrefix(rel string) bool {
	return len(rel) >= 3 && rel[0] == '.' && rel[1] == '.' && rel[2] == filepath.Separator
}

// confine resolves a path against every root this session has, returning
// the first that accepts it. The error is the cwd's, which is the one a
// person will recognise.
func (h *hosted) confine(p string) (string, error) {
	all := h.roots()
	abs, firstErr := wfs.New(all[0]).Confine(p)
	if firstErr == nil {
		return abs, nil
	}
	for _, r := range all[1:] {
		if abs, err := wfs.New(r).Confine(p); err == nil {
			return abs, nil
		}
	}
	return "", firstErr
}

// confineOrAsk is confine with a question instead of a refusal: a path
// outside every root asks the person, and a yes allows THAT path, once,
// without widening the session. Widening is a separate, deliberate act
// (addRoot) whose result is visible in the status bar; an approval buried
// in a stream of tool calls should not silently do it.
func (h *hosted) confineOrAsk(ctx context.Context, tool, p string) (string, error) {
	abs, err := h.confine(p)
	if err == nil {
		return abs, nil
	}
	outside, aerr := filepath.Abs(p)
	if aerr != nil {
		return "", err
	}
	outside = filepath.Clean(outside)
	log.Printf("agentd: acp %s OUTSIDE key=%s path=%q roots=%v — asking", tool, h.key, outside, h.roots())
	if !h.askOutside(ctx, tool, outside) {
		return "", err
	}
	return outside, nil
}

// sortedRoots is for logs and tests: the same set, order-independent.
func sortedRoots(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
