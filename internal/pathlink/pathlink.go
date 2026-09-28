// Package pathlink resolves the file references an agent writes in its
// transcript — `apps/edit/be/app.go:42`, `./run.sh`, `/abs/x.go:7:3` — to
// a file on disk, confined to the session's folder.
//
// The frontend finds the path-shaped tokens; this decides which of them
// are real. Both the Agent window and wash-edit's agent tabs answer their
// FE's probes with it, and resolve a clicked token again with it rather
// than trusting the path the FE sends back.
//
// Only regular files at or below the session folder resolve. A reference
// outside it, a directory, or a symlink that leads out is not a link.
package pathlink

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// MaxProbe caps one probe. A transcript renders a message at a time, and a
// message with more distinct file references than this is a listing nobody
// is going to click through.
const MaxProbe = 64

// Hit is a token that names a file: the absolute path, and the position
// the token carried (zero when it had none).
type Hit struct {
	Token string `json:"token"`
	Path  string `json:"path"`
	Line  int    `json:"line,omitempty"`
	Col   int    `json:"col,omitempty"`
}

// Split separates a trailing `:line` or `:line:col` from a token.
func Split(tok string) (path string, line, col int) {
	path = tok
	if i := strings.LastIndexByte(path, ':'); i > 0 {
		if n, err := strconv.Atoi(path[i+1:]); err == nil && n > 0 {
			path, line = path[:i], n
			if j := strings.LastIndexByte(path, ':'); j > 0 {
				if m, err := strconv.Atoi(path[j+1:]); err == nil && m > 0 {
					path, line, col = path[:j], m, n
				}
			}
		}
	}
	return path, line, col
}

// Resolve turns tok into a Hit when it names a regular file at or below
// base. Relative tokens are taken against base, `~/` against the home
// folder. base must be absolute; empty resolves nothing.
func Resolve(base, tok string) (Hit, bool) {
	if base == "" || !filepath.IsAbs(base) || tok == "" {
		return Hit{}, false
	}
	p, line, col := Split(tok)
	switch {
	case p == "~" || strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return Hit{}, false
		}
		p = filepath.Join(home, strings.TrimPrefix(p[1:], "/"))
	case filepath.IsAbs(p):
		p = filepath.Clean(p)
	default:
		p = filepath.Join(base, p)
	}
	base = filepath.Clean(base)
	if !within(base, p) {
		return Hit{}, false
	}
	// The same question again with links followed, so a symlink inside the
	// folder cannot lead a click outside it.
	realBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		return Hit{}, false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil || !within(realBase, real) {
		return Hit{}, false
	}
	if info, err := os.Stat(real); err != nil || !info.Mode().IsRegular() {
		return Hit{}, false
	}
	return Hit{Token: tok, Path: p, Line: line, Col: col}, true
}

// Probe resolves up to MaxProbe tokens and returns the ones that are files.
func Probe(base string, toks []string) []Hit {
	hits := []Hit{}
	for i, tok := range toks {
		if i >= MaxProbe {
			break
		}
		if h, ok := Resolve(base, tok); ok {
			hits = append(hits, h)
		}
	}
	return hits
}

// within reports whether p is base or below it; both are clean and absolute.
func within(base, p string) bool {
	rel, err := filepath.Rel(base, p)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
}
