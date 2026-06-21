package fswatch

import (
	"errors"
	"path/filepath"
	"strings"
	"sync"
)

// ErrNoRoute is returned by a Router with no default Source when a Watch path
// matches no registered mount.
var ErrNoRoute = errors.New("fswatch: no route for path")

// Router is a Source that dispatches each Watch to a registered sub-Source by
// path prefix. It is the seam that makes filesystem watching network-agnostic
// from a consumer's point of view: a path under a mounted remote tree routes to
// that mount's Source (e.g. a remotewatch.Client streaming events from the
// remote host), while every other path falls through to a default Source
// (normally the local inotify Manager via LocalSource). The consumer calls
// Watch and never learns which side answered.
//
// Ownership: the Router OWNS the mount Sources registered with it — Unmount and
// Close close them. It does NOT own the default Source; the caller created it
// and closes it.
type Router struct {
	mu     sync.RWMutex
	def    Source
	mounts []mountRoute
}

type mountRoute struct {
	prefix string // cleaned absolute mountpoint
	src    Source
}

var _ Source = (*Router)(nil)

// NewRouter returns a Router that sends unmatched paths to def (may be nil, in
// which case an unmatched Watch returns ErrNoRoute).
func NewRouter(def Source) *Router {
	return &Router{def: def}
}

// Mount registers src to serve watches for prefix and everything beneath it.
// Re-mounting the same prefix replaces (and closes) the previous Source.
func (r *Router) Mount(prefix string, src Source) {
	prefix = filepath.Clean(prefix)
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.mounts {
		if r.mounts[i].prefix == prefix {
			old := r.mounts[i].src
			r.mounts[i].src = src
			if old != nil {
				old.Close()
			}
			return
		}
	}
	r.mounts = append(r.mounts, mountRoute{prefix: prefix, src: src})
}

// Unmount removes and closes the Source registered for prefix. It reports
// whether a mount was present.
func (r *Router) Unmount(prefix string) bool {
	prefix = filepath.Clean(prefix)
	r.mu.Lock()
	var removed Source
	for i := range r.mounts {
		if r.mounts[i].prefix == prefix {
			removed = r.mounts[i].src
			r.mounts = append(r.mounts[:i], r.mounts[i+1:]...)
			break
		}
	}
	r.mu.Unlock()
	if removed != nil {
		removed.Close()
		return true
	}
	return false
}

// Watch routes path to the longest-matching mount Source, or the default.
func (r *Router) Watch(path string) (Subscription, error) {
	src := r.route(filepath.Clean(path))
	if src == nil {
		return nil, ErrNoRoute
	}
	return src.Watch(path)
}

// route returns the Source for path: the mount with the longest matching prefix
// (so nested mounts resolve to the innermost), else the default.
func (r *Router) route(path string) Source {
	r.mu.RLock()
	defer r.mu.RUnlock()
	best := -1
	var bestSrc Source
	for _, m := range r.mounts {
		if isUnder(m.prefix, path) && len(m.prefix) > best {
			best, bestSrc = len(m.prefix), m.src
		}
	}
	if bestSrc != nil {
		return bestSrc
	}
	return r.def
}

// Close closes every registered mount Source. It leaves the default alone (the
// caller owns it).
func (r *Router) Close() error {
	r.mu.Lock()
	mounts := r.mounts
	r.mounts = nil
	r.mu.Unlock()
	for _, m := range mounts {
		if m.src != nil {
			m.src.Close()
		}
	}
	return nil
}

// isUnder reports whether path is prefix or sits beneath it.
func isUnder(prefix, path string) bool {
	if prefix == path {
		return true
	}
	if prefix == "/" {
		return strings.HasPrefix(path, "/")
	}
	return strings.HasPrefix(path, prefix+"/")
}
