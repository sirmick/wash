package edit

// Desktop-wide editor preferences: $XDG_CONFIG_HOME/wash/edit.json.
//
// Window state (open tabs, cursor, split) is per window and lives in the
// router's app_state (sdk.HandlePersist). Preferences — font size, the
// default indentation, save-time cleanups, the recent-files list — are
// desktop-wide, so they live in a file every editor window shares.
//
// The file is an object the FE owns the shape of; the BE only merges
// and stores it, so a new preference is one FE-side key, not a schema
// change here. Writes are read-merge-write under a process mutex and an
// atomic rename, so two windows updating different keys at the same
// moment lose nothing but the race between two writes of the SAME key.
//
// Wire shape:
//
//	FE → BE  : { kind: "prefs" }                       → prefs_ok { prefs }
//	           { kind: "prefs_set", patch: {…} }       → prefs_set_ok { prefs }
//	           { kind: "recent_add", path }            → recent_add_ok { recent }
//	           { kind: "recent_drop", path }           → recent_drop_ok { recent }

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
)

const (
	// prefsMaxBytes bounds the file so a runaway patch cannot turn the
	// config dir into a dumping ground.
	prefsMaxBytes = 64 * 1024
	// recentCap mirrors the FE's RECENT_CAP (quick-open.ts).
	recentCap = 20
)

var prefsMu sync.Mutex

// prefsPath resolves $XDG_CONFIG_HOME/wash/edit.json, falling back to
// ~/.config/wash/edit.json. Empty when neither is known.
func prefsPath() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash", "edit.json")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash", "edit.json")
	}
	return ""
}

// loadPrefs reads the file; a missing file is an empty object, and a
// corrupt one is treated the same (logged) rather than wedging every
// editor window on a stray byte.
func loadPrefs() map[string]any {
	out := map[string]any{}
	path := prefsPath()
	if path == "" {
		return out
	}
	buf, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("wash-edit: prefs read path=%q: %v", path, err)
		}
		return out
	}
	if err := json.Unmarshal(buf, &out); err != nil {
		log.Printf("wash-edit: prefs parse path=%q: %v", path, err)
		return map[string]any{}
	}
	return out
}

// storePrefs atomically replaces the file (temp + fsync + rename, so a
// window reading it mid-write sees the old or the new, never a torn one).
func storePrefs(p map[string]any) error {
	path := prefsPath()
	if path == "" {
		return sdk.Errf(sdk.ErrIO, "no config dir")
	}
	out, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return sdk.Err{Code: sdk.ErrBadRequest, Msg: err.Error()}
	}
	if len(out) > prefsMaxBytes {
		return sdk.Errf("too_large", "prefs exceed cap")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	tmp, err := os.CreateTemp(dir, ".edit-*.json.tmp")
	if err != nil {
		return sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	tmpPath := tmp.Name()
	fail := func(err error) error {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	if _, err := tmp.Write(out); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return sdk.Err{Code: sdk.ErrIO, Msg: err.Error()}
	}
	return nil
}

type prefsReply struct {
	Prefs map[string]any `json:"prefs"`
}

type prefsSetReq struct {
	Patch map[string]any `json:"patch"`
}

type recentReq struct {
	Path string `json:"path"`
}

type recentReply struct {
	Recent []string `json:"recent"`
}

// recentOf reads the recent list out of a prefs map, dropping anything
// that is not a non-empty string.
func recentOf(p map[string]any) []string {
	raw, _ := p["recent"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}

// pushRecent is the BE twin of quick-open.ts's pushRecent: path first,
// deduplicated, capped.
func pushRecent(recent []string, path string) []string {
	out := []string{path}
	for _, p := range recent {
		if p == path {
			continue
		}
		out = append(out, p)
		if len(out) >= recentCap {
			break
		}
	}
	return out
}

func dropRecent(recent []string, path string) []string {
	out := recent[:0:0]
	for _, p := range recent {
		if p != path {
			out = append(out, p)
		}
	}
	return out
}

func registerPrefsHandlers(b *sdk.Bus) {
	sdk.Handle(b, "prefs", func(_ *sdk.Conn, _ string, _ struct{}) (prefsReply, error) {
		prefsMu.Lock()
		defer prefsMu.Unlock()
		return prefsReply{Prefs: loadPrefs()}, nil
	})

	sdk.Handle(b, "prefs_set", func(_ *sdk.Conn, _ string, req prefsSetReq) (prefsReply, error) {
		prefsMu.Lock()
		defer prefsMu.Unlock()
		p := loadPrefs()
		keys := make([]string, 0, len(req.Patch))
		for k, v := range req.Patch {
			// A null clears the key; anything else replaces it. The
			// recent list has its own verbs and is never patched here,
			// so a stale window cannot overwrite a fresher list.
			if k == "recent" {
				continue
			}
			keys = append(keys, k)
			if v == nil {
				delete(p, k)
			} else {
				p[k] = v
			}
		}
		if err := storePrefs(p); err != nil {
			log.Printf("wash-edit: prefs set failed: %v", err)
			return prefsReply{}, err
		}
		sort.Strings(keys)
		log.Printf("wash-edit: prefs set keys=%v", keys)
		return prefsReply{Prefs: p}, nil
	})

	sdk.Handle(b, "recent_add", func(_ *sdk.Conn, _ string, req recentReq) (recentReply, error) {
		if req.Path == "" {
			return recentReply{}, sdk.Errf(sdk.ErrBadRequest, "missing path")
		}
		prefsMu.Lock()
		defer prefsMu.Unlock()
		p := loadPrefs()
		recent := pushRecent(recentOf(p), req.Path)
		p["recent"] = recent
		if err := storePrefs(p); err != nil {
			return recentReply{}, err
		}
		return recentReply{Recent: recent}, nil
	})

	sdk.Handle(b, "recent_drop", func(_ *sdk.Conn, _ string, req recentReq) (recentReply, error) {
		prefsMu.Lock()
		defer prefsMu.Unlock()
		p := loadPrefs()
		recent := dropRecent(recentOf(p), req.Path)
		p["recent"] = recent
		if err := storePrefs(p); err != nil {
			return recentReply{}, err
		}
		return recentReply{Recent: recent}, nil
	})
}
