// Desktop config: ~/.config/wash/desktop.json. Read at startup,
// watched via internal/fswatch (the directory, not the file itself —
// settings.write uses temp+rename which destroys the inode), shipped
// to the FE as a "desktop.config" app_msg carrying the parsed prefs
// plus the wallpaper bytes inline (CBOR carries binary natively).
//
// One consumer (wash-session FE) — per [no premature service] this
// stays a session-app private concern; wash-settings only writes the
// file, never reads it back at runtime.
package session

import (
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/internal/sdk"
)

// maxWallpaperBytes caps inline image payload. Frames are 16 MiB;
// leaving headroom for the CBOR map overhead.
const maxWallpaperBytes = 12 * 1024 * 1024

// desktopConfig is the on-disk schema. Every field is optional; the
// FE applies defaults for missing values so an empty file behaves
// the same as a never-written one.
type desktopConfig struct {
	Wallpaper wallpaperPref `json:"wallpaper"`
	Clock     clockPref     `json:"clock"`
	Taskbar   taskbarPref   `json:"taskbar"`
}

type wallpaperPref struct {
	Path          string `json:"path,omitempty"`
	Mode          string `json:"mode,omitempty"`           // cover | contain | tile | center
	FallbackColor string `json:"fallback_color,omitempty"` // hex, used when no path
}

type clockPref struct {
	Format      string `json:"format,omitempty"` // "12h" | "24h"
	ShowSeconds bool   `json:"show_seconds,omitempty"`
}

type taskbarPref struct {
	Position string `json:"position,omitempty"` // "top" | "bottom"
}

// configDir resolves $XDG_CONFIG_HOME/wash, falling back to
// $HOME/.config/wash. Returns "" only if both env vars are unset
// (very unusual; the BE just skips watching in that case).
func configDir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "wash")
	}
	if h, err := os.UserHomeDir(); err == nil {
		return filepath.Join(h, ".config", "wash")
	}
	return ""
}

func configFilePath() string {
	d := configDir()
	if d == "" {
		return ""
	}
	return filepath.Join(d, "desktop.json")
}

// loadConfig reads desktop.json. A missing file is not an error —
// returns a zero-value config so the FE applies built-in defaults.
func loadConfig() (desktopConfig, error) {
	var cfg desktopConfig
	path := configFilePath()
	if path == "" {
		return cfg, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, err
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// readWallpaper returns the bytes of cfg.Wallpaper.Path. With no
// path configured — or the configured file unreadable — it falls
// back to the embedded default-wallpaper.png so a fresh install
// still has a wallpaper out of the box.
func readWallpaper(cfg desktopConfig) ([]byte, string) {
	path := cfg.Wallpaper.Path
	if path == "" {
		return defaultWallpaper, "image/png"
	}
	st, err := os.Stat(path)
	if err != nil {
		log.Printf("wash-session: wallpaper stat %s: %v (using default)", path, err)
		return defaultWallpaper, "image/png"
	}
	if st.Size() > maxWallpaperBytes {
		log.Printf("wash-session: wallpaper %s exceeds %d bytes (using default)", path, maxWallpaperBytes)
		return defaultWallpaper, "image/png"
	}
	data, err := os.ReadFile(path)
	if err != nil {
		log.Printf("wash-session: wallpaper read %s: %v (using default)", path, err)
		return defaultWallpaper, "image/png"
	}
	return data, mimeFromExt(path)
}

// mimeFromExt is a tiny extension→mime lookup. The FE only needs
// enough to construct a Blob; the browser handles decode either way,
// but a correct type makes devtools nicer.
func mimeFromExt(path string) string {
	switch filepath.Ext(path) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".avif":
		return "image/avif"
	case ".svg":
		return "image/svg+xml"
	}
	return "application/octet-stream"
}

// sendDesktopConfig assembles + ships the current config state to
// the FE. Called once at startup and once per fswatch event.
func sendDesktopConfig(c *sdk.Conn) {
	cfg, err := loadConfig()
	if err != nil {
		log.Printf("wash-session: load config: %v", err)
	}
	bytes, mime := readWallpaper(cfg)
	msg := map[string]any{
		"kind": "desktop.config",
		"wallpaper": map[string]any{
			"mode":           cfg.Wallpaper.Mode,
			"fallback_color": cfg.Wallpaper.FallbackColor,
			"mime":           mime,
			"bytes":          bytes, // nil when no image; CBOR ⇒ null
		},
		"clock": map[string]any{
			"format":       cfg.Clock.Format,
			"show_seconds": cfg.Clock.ShowSeconds,
		},
		"taskbar": map[string]any{
			"position": cfg.Taskbar.Position,
		},
	}
	if err := c.SendAppMsg(msg); err != nil {
		log.Printf("wash-session: send desktop.config: %v", err)
	}
}

// startConfigWatcher watches the config DIRECTORY (atomic temp+
// rename writes don't survive a file-level inotify watch) and re-
// sends desktop.config on every event. Coalesces bursts via a small
// debounce — settings.write is one rename but editors can fire
// multiple events; we don't want to ship the image twice in 50ms.
func startConfigWatcher(c *sdk.Conn) {
	dir := configDir()
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("wash-session: mkdir %s: %v", dir, err)
		return
	}
	mgr, err := fswatch.New()
	if err != nil {
		log.Printf("wash-session: fswatch unavailable: %v", err)
		return
	}
	sub, err := mgr.Watch(dir)
	if err != nil {
		log.Printf("wash-session: watch %s: %v", dir, err)
		mgr.Close()
		return
	}
	target := configFilePath()
	go func() {
		var (
			mu      sync.Mutex
			pending bool
		)
		for ev := range sub.Events() {
			if ev.Path != target {
				continue
			}
			mu.Lock()
			if pending {
				mu.Unlock()
				continue
			}
			pending = true
			mu.Unlock()
			time.AfterFunc(60*time.Millisecond, func() {
				mu.Lock()
				pending = false
				mu.Unlock()
				sendDesktopConfig(c)
			})
		}
	}()
}
