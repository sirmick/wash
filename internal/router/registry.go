package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
)

// reservedIDs are app ids the router refuses to serve from an
// untrusted (user-writable) binary. v0.1 has exactly one entry —
// com.wash.priv, whose red-stripe titlebar treatment depends on no
// other binary being able to claim that id. A user-writable shadow
// would let any local app inherit wash-priv's "this is the
// privilege chain" trust signal.
//
// To add a reserved id later, append it here and document the
// reason at the call site that depends on the guarantee.
var reservedIDs = map[string]bool{
	"com.wash.priv": true,
}

// Entry is one row of the catalog.
//
// When the manifest probe failed or the manifest does not validate,
// Manifest may be nil and Reason holds the human-readable cause. The
// app is then listed-disabled, NEVER silently dropped (WIRE.md §5.1).
type Entry struct {
	Path     string
	Manifest *Manifest
	Reason   string // empty when valid
}

// Enabled reports whether the entry is usable (has a manifest and no
// disable reason).
func (e *Entry) Enabled() bool { return e.Manifest != nil && e.Reason == "" }

// Registry holds the catalog of discovered apps. Built once at router
// startup; live rescan is post-v0.0.
type Registry struct {
	mu      sync.RWMutex
	byID    map[string]*Entry
	entries []*Entry // stable order; user dir wins on id collision

	// trustedDirs are paths under which a binary's uid-0 ownership
	// requirement is relaxed: the binary still must not be world- or
	// group-writable, but its owner may be the user running the
	// router. Used for dev environments where wash-priv is built into
	// the same out/ dir as wash-router. Empty in production.
	trustedDirs []string
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]*Entry)}
}

// SetTrustedDirs declares additional directories under which binaries
// are accepted as "trusted" for the purpose of serving reservedIDs,
// even when not owned by uid 0. Must be called before Scan. Pass
// absolute paths; relative paths are silently dropped.
func (r *Registry) SetTrustedDirs(dirs []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.trustedDirs = nil
	for _, d := range dirs {
		if filepath.IsAbs(d) {
			r.trustedDirs = append(r.trustedDirs, filepath.Clean(d))
		}
	}
}

// Scan walks dirs in order, probes every +x regular file, and
// populates the registry. Later occurrences of the same id do NOT
// override earlier ones — caller orders dirs so user paths come first
// (WIRE.md §5).
func (r *Registry) Scan(ctx context.Context, dirs []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, dir := range dirs {
		bins, err := executablesIn(dir)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("scan %s: %w", dir, err)
		}
		for _, bin := range bins {
			r.probeAndRegister(ctx, bin)
		}
	}
	return nil
}

func (r *Registry) probeAndRegister(ctx context.Context, bin string) {
	data, err := Probe(ctx, bin)
	if err != nil {
		// We still add the entry so it can be surfaced.
		r.appendEntry(&Entry{Path: bin, Reason: err.Error()})
		return
	}
	m, valErr := ParseManifest(data)
	entry := &Entry{Path: bin, Manifest: m}
	if valErr != nil {
		entry.Reason = valErr.Error()
	}
	r.appendEntry(entry)
}

func (r *Registry) appendEntry(e *Entry) {
	if e.Manifest != nil {
		// Reserved-id gate: a user-writable binary cannot claim a
		// reserved id. Mark listed-disabled (never silently dropped)
		// so the operator can see why the launcher entry is missing.
		if reservedIDs[e.Manifest.ID] && !r.isTrustedBinary(e.Path) {
			e.Reason = fmt.Sprintf("reserved id %q requires a trusted binary (root-owned, owned by this user with mode 0755, or under a trusted dir; not world/group-writable)", e.Manifest.ID)
		}
		if existing, ok := r.byID[e.Manifest.ID]; ok {
			// First registration wins (user dir scanned first).
			_ = existing
			return
		}
		r.byID[e.Manifest.ID] = e
	}
	r.entries = append(r.entries, e)
}

// isTrustedBinary reports whether path is acceptable as a host for a
// reservedID. Three branches:
//
//   (a) Root-owned strict path: file owned by uid 0 AND not group- or
//       world-writable. The production posture — root install under
//       /usr/share/wash/apps/.
//   (b) Same-user strict path: file owned by the uid running the
//       router AND not group- or world-writable. On a single-user
//       wash setup, the threat model already assumes apps run as the
//       user; the meaningful defence against a malicious local app
//       planting a fake wash-priv is "this file isn't writable by
//       other accounts on the box." Owner-uid + 0755 perms is
//       exactly that, and lets `make all` produce binaries that
//       Just Work without flags or env vars.
//   (c) Trusted-dir relaxed path: the file lives directly under one
//       of the registry's trustedDirs and is not world-writable.
//       Group-writability is accepted via this path (default umask
//       0002 yields 0775); the dir declaration IS the trust
//       statement. World-write is still a hard fail.
//
// Stat failures fail closed.
func (r *Registry) isTrustedBinary(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	if sys.Uid == 0 && info.Mode().Perm()&0o022 == 0 {
		return true
	}
	if int(sys.Uid) == os.Getuid() && info.Mode().Perm()&0o022 == 0 {
		return true
	}
	if info.Mode().Perm()&0o002 != 0 {
		return false
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	abs = filepath.Clean(abs)
	dir := filepath.Dir(abs)
	for _, td := range r.trustedDirs {
		if dir == td {
			return true
		}
	}
	return false
}

// ByID returns the entry for the given id or nil.
func (r *Registry) ByID(id string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
}

// FindByBasename returns the enabled entry whose binary's basename
// matches name, or nil. Used by the wash-sudo bare-invocation
// heuristic: `wash-sudo wash-term` auto-promotes to spawn mode when
// "wash-term" is the basename of a registered wash app binary.
// First match wins; the registry's stable scan order makes that
// deterministic per session.
func (r *Registry) FindByBasename(name string) *Entry {
	if name == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if !e.Enabled() {
			continue
		}
		if filepath.Base(e.Path) == name {
			return e
		}
	}
	return nil
}

// Entries returns a stable snapshot of all entries (enabled + disabled).
func (r *Registry) Entries() []*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]*Entry, len(r.entries))
	copy(out, r.entries)
	return out
}

// executablesIn returns the absolute paths of regular +x files in dir,
// in name order. Symlinks are followed only enough to stat their target.
func executablesIn(dir string) ([]string, error) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		if info.Mode().Perm()&0o111 == 0 {
			continue
		}
		out = append(out, filepath.Join(dir, e.Name()))
	}
	sort.Strings(out)
	return out, nil
}

// SplitAppsDir splits a WASH_APPS_DIR-style colon-separated list into
// individual directories, ignoring empty entries.
func SplitAppsDir(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ":") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
