package session

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Launcher state — the start menu's "Recent" files and "Pinned" apps.
//
// Both live in ONE file, $XDG_STATE_HOME/wash/recent.json (fallback
// ~/.local/state/wash/recent.json): the two lists are the launcher's memory
// of what the person reaches for, and they change on the same gestures. The
// BE owns it (not the router's per-instance persist blob) because it must
// outlive the session process and be readable by tests and humans.
//
// Recent is fed two ways. The router's open.routed notice (internal/
// router/open_routed.go) covers every open.request it routed, every
// spawn.request that carried a launch path, every terminal-launched
// `wash-edit --open`. Apps add the rest themselves with recent.note
// (launcher_bus.go): fm the folder it was showing when it closed, Radio
// the station it played — things no open request ever describes.
//
// Newest first. Path entries dedupe by path; name entries (a station has
// no path) by app and name. The cap is per app, so a morning of radio
// cannot push yesterday's documents out. Entries whose file is gone are
// dropped at READ time (not at write time) — a file on a mount that comes
// and goes should reappear when the mount does, so nothing is forgotten
// until the person removes it or it ages out of the cap.

const maxRecentPerApp = 10

type recentEntry struct {
	Path string `json:"path,omitempty"`
	// Name identifies an entry that is not a file — a radio station. Set
	// instead of Path, never with it.
	Name  string    `json:"name,omitempty"`
	AppID string    `json:"app_id"`
	At    time.Time `json:"at"`
}

// key is the dedupe identity: the path for a file or folder, the app and
// name for anything else.
func (e recentEntry) key() string {
	if e.Path != "" {
		return "p\x00" + e.Path
	}
	return "n\x00" + e.AppID + "\x00" + e.Name
}

// launcherState is the on-disk shape. Pinned is app ids in pin order.
type launcherState struct {
	Recent []recentEntry `json:"recent"`
	Pinned []string      `json:"pinned"`
}

type launcherStore struct {
	mu   sync.Mutex
	path string // "" = memory only (no HOME/XDG resolvable)
	st   launcherState
	// exists is the file-presence probe Snapshot uses; swappable for tests.
	exists func(path string) bool
}

// launcherStatePath resolves the state file via XDG_STATE_HOME or its
// ~/.local/state fallback. "" when neither HOME nor XDG_STATE_HOME is set —
// the store then works in memory for the session's lifetime.
func launcherStatePath() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "wash", "recent.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "wash", "recent.json")
}

// newLauncherStore loads path (missing file = empty state; a corrupt file is
// logged and treated as empty rather than wedging the desktop).
func newLauncherStore(path string) *launcherStore {
	s := &launcherStore{path: path, exists: fileExists}
	if path == "" {
		return s
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("wash-session: launcher state %s: %v", path, err)
		}
		return s
	}
	if err := json.Unmarshal(raw, &s.st); err != nil {
		log.Printf("wash-session: launcher state %s: corrupt (%v); starting empty", path, err)
		s.st = launcherState{}
	}
	s.st.Recent = normalizeRecent(s.st.Recent)
	return s
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// normalizeRecent dedupes (first occurrence wins — the slice is sorted
// newest-first first) and applies the per-app cap.
func normalizeRecent(in []recentEntry) []recentEntry {
	sort.SliceStable(in, func(i, j int) bool { return in[i].At.After(in[j].At) })
	seen := map[string]bool{}
	perApp := map[string]int{}
	out := make([]recentEntry, 0, len(in))
	for _, e := range in {
		if (e.Path == "" && e.Name == "") || seen[e.key()] || perApp[e.AppID] >= maxRecentPerApp {
			continue
		}
		seen[e.key()] = true
		perApp[e.AppID]++
		out = append(out, e)
	}
	return out
}

// Note records an open of path by appID as the newest entry.
func (s *launcherStore) Note(path, appID string) {
	if path == "" {
		return
	}
	s.note(recentEntry{Path: path, AppID: appID})
}

// NoteName records a non-file item (a radio station) by appID.
func (s *launcherStore) NoteName(name, appID string) {
	if name == "" || appID == "" {
		return
	}
	s.note(recentEntry{Name: name, AppID: appID})
}

func (s *launcherStore) note(e recentEntry) {
	e.At = time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Recent = normalizeRecent(append([]recentEntry{e}, s.st.Recent...))
	s.saveLocked()
}

// Remove forgets one entry: by path, or by name within appID when path is
// empty.
func (s *launcherStore) Remove(path, name, appID string) {
	target := recentEntry{Path: path, Name: name, AppID: appID}
	if path == "" && name == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.st.Recent[:0]
	for _, e := range s.st.Recent {
		if e.key() != target.key() {
			kept = append(kept, e)
		}
	}
	s.st.Recent = kept
	s.saveLocked()
}

// Clear forgets every recent path (pins are untouched).
func (s *launcherStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st.Recent = nil
	s.saveLocked()
}

// SetPinned pins (on) or unpins appID. Pin order is append order; a re-pin
// of an already-pinned app is a no-op (keeps its slot).
func (s *launcherStore) SetPinned(appID string, on bool) {
	if appID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]string, 0, len(s.st.Pinned)+1)
	already := false
	for _, id := range s.st.Pinned {
		if id == appID {
			already = true
			if !on {
				continue
			}
		}
		kept = append(kept, id)
	}
	if on && !already {
		kept = append(kept, appID)
	}
	s.st.Pinned = kept
	s.saveLocked()
}

// Snapshot returns the state with vanished files filtered out of Recent
// (name entries have no file to vanish).
// The filter is read-side only: the entries stay on disk (see the file
// comment). Pinned is copied so callers can't alias the store.
func (s *launcherStore) Snapshot() launcherState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := launcherState{Recent: make([]recentEntry, 0, len(s.st.Recent)), Pinned: append([]string{}, s.st.Pinned...)}
	for _, e := range s.st.Recent {
		if e.Path == "" || s.exists(e.Path) {
			out.Recent = append(out.Recent, e)
		}
	}
	return out
}

// saveLocked writes the file atomically (tmp + rename). Caller holds mu.
func (s *launcherStore) saveLocked() {
	if s.path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		log.Printf("wash-session: launcher state mkdir: %v", err)
		return
	}
	// Always arrays on disk, never null: readers (the FE, tests, a human
	// with jq) should not need a nil branch for an empty list.
	st := s.st
	if st.Recent == nil {
		st.Recent = []recentEntry{}
	}
	if st.Pinned == nil {
		st.Pinned = []string{}
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		log.Printf("wash-session: launcher state encode: %v", err)
		return
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		log.Printf("wash-session: launcher state write: %v", err)
		return
	}
	if err := os.Rename(tmp, s.path); err != nil {
		log.Printf("wash-session: launcher state rename: %v", err)
	}
}
