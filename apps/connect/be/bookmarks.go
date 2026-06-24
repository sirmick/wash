package connect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirmick/wash/internal/sdk"
)

// Bookmarks are long-lived user data (host + optional app to launch), so
// unlike window/app FE state — which is router-memory and dies on a
// router restart — they live on disk under $XDG_CONFIG_HOME/wash/, the
// same place wash-settings keeps desktop.json. wash-connect reads them on
// the FE's bookmarks_load and rewrites the file on bookmarks_save.

const maxBookmarksBytes = 64 * 1024

// Bookmark is one saved connect target plus its per-host preferences.
// Label is the FE's display string. AutoApps / AutoMounts are replayed by
// the FE the moment the host reaches "up": each app id is launched and each
// remote path is mounted, so a saved host can bring its working set back
// with one Connect. AppID is the legacy single-app field (pre-AutoApps),
// kept for on-disk back-compat; the FE migrates it into AutoApps on load.
type Bookmark struct {
	Host       string   `json:"host"`
	Label      string   `json:"label,omitempty"`
	AutoApps   []string `json:"auto_apps,omitempty"`
	AutoMounts []string `json:"auto_mounts,omitempty"`
	AppID      string   `json:"app_id,omitempty"` // legacy; migrated to AutoApps
}

// bookmarksMu serializes the read-modify-write the FE drives; concurrent
// saves from a flaky FE shouldn't tear the file.
var bookmarksMu sync.Mutex

// configDir resolves $XDG_CONFIG_HOME/wash, falling back to
// $HOME/.config/wash. Mirrors wash-settings' resolver (kept local — the
// contract is the path, and a single-consumer helper belongs in its
// consumer). Returns "" only if both env vars are unset.
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

func bookmarksFile() string {
	dir := configDir()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "connect.json")
}

// loadBookmarks reads the bookmarks file. A missing file is not an error
// (no bookmarks yet) — it returns an empty slice.
func loadBookmarks() ([]Bookmark, error) {
	path := bookmarksFile()
	if path == "" {
		return nil, sdk.Errf("io", "no config dir (HOME/XDG_CONFIG_HOME unset)")
	}
	bookmarksMu.Lock()
	defer bookmarksMu.Unlock()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return []Bookmark{}, nil
	}
	if err != nil {
		return nil, sdk.Err{Code: "io", Msg: err.Error()}
	}
	var bms []Bookmark
	if err := json.Unmarshal(data, &bms); err != nil {
		return nil, sdk.Err{Code: "bad_request", Msg: err.Error()}
	}
	return bms, nil
}

// saveBookmarks atomically replaces the bookmarks file (temp + rename).
func saveBookmarks(bms []Bookmark) error {
	path := bookmarksFile()
	if path == "" {
		return sdk.Errf("io", "no config dir (HOME/XDG_CONFIG_HOME unset)")
	}
	out, err := json.MarshalIndent(bms, "", "  ")
	if err != nil {
		return sdk.Err{Code: "bad_request", Msg: err.Error()}
	}
	if len(out) > maxBookmarksBytes {
		return sdk.Errf("too_large", "bookmarks exceed cap")
	}
	bookmarksMu.Lock()
	defer bookmarksMu.Unlock()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	tmp, err := os.CreateTemp(dir, ".connect-*.json.tmp")
	if err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath) // no-op after a successful rename
	if _, err := tmp.Write(out); err != nil {
		tmp.Close()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := tmp.Close(); err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return sdk.Err{Code: "io", Msg: err.Error()}
	}
	return nil
}
