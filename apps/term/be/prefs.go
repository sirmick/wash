// Terminal preferences — desktop-wide, not per-window.
//
// Font, palette, smart-paste mode, cursor and scrollback used to live in
// each window's persisted FE blob, which meant they were per WINDOW: set a
// font, open a second terminal, get the default back. Nobody wants a
// terminal whose appearance depends on which one they happened to open.
//
// They live in $XDG_CONFIG_HOME/wash/term.json (~/.config/wash/term.json),
// the same layout apps/settings/be's domain files and agents.json use. The
// file is also the BROADCAST channel: wash-term is InstancingMulti, so each
// window is a separate process and none of them can message the others
// directly. Each watches the file's directory and pushes the new prefs to
// its own FE when it changes, so a change in one window reaches the rest
// through the thing that already had to be the source of truth.
package term

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirmick/wash/internal/fswatch"
	"github.com/sirmick/wash/pkg/sdk"
)

// prefsDomain names the file. Kept in step with the settings-domain naming
// (<domain>.json) so a settings pane can adopt it later without a move.
const prefsDomain = "term"

// termPrefs is the stored blob. Every field is omitempty and every reader
// treats an absent field as "the FE's default": a file written by an older
// build, or hand-edited down to one key, must not reset the rest.
type termPrefs struct {
	FontID      string `json:"font_id,omitempty"`
	FontSize    int    `json:"font_size,omitempty"`
	ThemeID     string `json:"theme_id,omitempty"`
	SmartPaste  string `json:"smart_paste,omitempty"`
	CursorStyle string `json:"cursor_style,omitempty"`
	// CursorBlink is a pointer because false is a meaningful value that
	// omitempty would otherwise erase on every write.
	CursorBlink *bool `json:"cursor_blink,omitempty"`
	Scrollback  int   `json:"scrollback,omitempty"`
}

// prefsPath is ~/.config/wash/term.json, XDG_CONFIG_HOME aware. Empty when
// there is no home directory to put it in, which disables persistence
// rather than failing anything.
func prefsPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "wash", prefsDomain+".json")
}

// loadPrefs reads the file. Any problem — missing, unreadable, malformed —
// yields the zero value, i.e. "no preference stated", which every consumer
// already handles. A half-parsed file must never half-apply.
func loadPrefs(path string) termPrefs {
	if path == "" {
		return termPrefs{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return termPrefs{}
	}
	var p termPrefs
	if err := json.Unmarshal(data, &p); err != nil {
		log.Printf("wash-term: prefs %s: %v (ignored)", path, err)
		return termPrefs{}
	}
	return p
}

// mergePrefs folds a partial update over the stored blob. Only the keys the
// FE actually sent move — a window that only knows about the font must not
// wipe the cursor style a newer build wrote.
func mergePrefs(base termPrefs, patch termPrefs) termPrefs {
	out := base
	if patch.FontID != "" {
		out.FontID = patch.FontID
	}
	if patch.FontSize > 0 {
		out.FontSize = patch.FontSize
	}
	if patch.ThemeID != "" {
		// "auto" is how the FE says "no pinned palette"; store it as
		// absent so a fresh file and a reset file read the same.
		if patch.ThemeID == "auto" {
			out.ThemeID = ""
		} else {
			out.ThemeID = patch.ThemeID
		}
	}
	if patch.SmartPaste != "" {
		out.SmartPaste = patch.SmartPaste
	}
	if patch.CursorStyle != "" {
		out.CursorStyle = patch.CursorStyle
	}
	if patch.CursorBlink != nil {
		v := *patch.CursorBlink
		out.CursorBlink = &v
	}
	if patch.Scrollback > 0 {
		out.Scrollback = patch.Scrollback
	}
	return out
}

// savePrefs writes atomically (temp + rename in the same directory), so a
// window reading the file while another writes it sees one version or the
// other and never a truncated one. The rename is also what the watchers
// see, which is why they watch the DIRECTORY rather than the file: an
// inotify watch on the old inode would go deaf after the first save.
func savePrefs(path string, p termPrefs) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".term-prefs-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// prefsMsg is the BE→FE push. `prefs` is sent once at ready and again on
// every change, whoever made it.
type prefsMsg struct {
	Kind  string    `json:"kind"`
	Prefs termPrefs `json:"prefs"`
}

// prefsState guards the last blob this process pushed, so a watch event
// caused by our OWN write doesn't bounce back to the FE that just made the
// change (which would fight the user mid-drag on the size stepper).
var prefsState struct {
	mu   sync.Mutex
	last string
}

// pushPrefs sends the current file contents to this window's FE, unless it
// is byte-for-byte what we last sent.
func pushPrefs(c *sdk.Conn, force bool) {
	p := loadPrefs(prefsPath())
	enc, err := json.Marshal(p)
	if err != nil {
		return
	}
	prefsState.mu.Lock()
	same := string(enc) == prefsState.last
	prefsState.last = string(enc)
	prefsState.mu.Unlock()
	if same && !force {
		return
	}
	if err := c.SendAppMsg(prefsMsg{Kind: "prefs", Prefs: p}); err != nil {
		log.Printf("wash-term: prefs push: %v", err)
	}
}

// setPrefs applies an FE patch: re-read, merge, write. Read-modify-write
// against the FILE rather than an in-memory copy, because the other writers
// are other wash-term processes — re-reading immediately before the write
// keeps the losing window to the length of this call.
func setPrefs(c *sdk.Conn, patch termPrefs) {
	path := prefsPath()
	next := mergePrefs(loadPrefs(path), patch)
	if err := savePrefs(path, next); err != nil {
		log.Printf("wash-term: prefs save: %v", err)
		return
	}
	pushPrefs(c, true)
}

// watchPrefs pushes the file to this window's FE whenever it changes on
// disk — the cross-window broadcast. It watches the containing directory,
// not the file: savePrefs renames into place, and a watch on the old inode
// would go deaf after the first save.
func watchPrefs(c *sdk.Conn) {
	path := prefsPath()
	if path == "" {
		return
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("wash-term: prefs watch mkdir: %v", err)
		return
	}
	mgr, err := fswatch.New()
	if err != nil {
		log.Printf("wash-term: prefs watch: %v", err)
		return
	}
	sub, err := mgr.Watch(dir)
	if err != nil {
		log.Printf("wash-term: prefs watch %s: %v", dir, err)
		mgr.Close()
		return
	}
	go func() {
		defer mgr.Close()
		defer sub.Close()
		for {
			select {
			case <-c.Done():
				return
			case ev, ok := <-sub.Events():
				if !ok {
					return
				}
				if filepath.Base(ev.Path) != filepath.Base(path) {
					continue
				}
				pushPrefs(c, false)
			}
		}
	}()
}
