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
)

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
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{byID: make(map[string]*Entry)}
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
		if existing, ok := r.byID[e.Manifest.ID]; ok {
			// First registration wins (user dir scanned first).
			_ = existing
			return
		}
		r.byID[e.Manifest.ID] = e
	}
	r.entries = append(r.entries, e)
}

// ByID returns the entry for the given id or nil.
func (r *Registry) ByID(id string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byID[id]
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
