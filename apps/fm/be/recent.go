package fm

import (
	"log"
	"os"
	"path/filepath"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// The start menu's Files flyout lists the folders fm was showing when its
// windows closed. Only fm knows that: the router sees the folder a window
// was LAUNCHED on (and notes it), never where the person went afterwards.

const sessionAppID = "com.wash.session"

type persistReq struct {
	State map[string]any `json:"state"`
}

// shownDir is the folder this window last showed, from the FE's persisted
// view state. The FE commits a navigation eagerly (selectPath persists
// before any listing loads), so it is current at close.
var shownDir lastDir

type lastDir struct {
	mu   sync.Mutex
	path string
}

func (d *lastDir) set(p string) {
	d.mu.Lock()
	d.path = p
	d.mu.Unlock()
}

func (d *lastDir) get() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.path
}

// closingFolder is the folder to record: the one shown, or the launch
// folder when the window never navigated. A selected file stands for its
// parent. Anything outside the root or no longer there records nothing.
func closingFolder(shown string) string {
	p := shown
	if p == "" {
		p = initialPath()
	}
	if p == "" || fmFS == nil {
		return ""
	}
	abs, err := fmFS.Confine(p)
	if err != nil {
		return ""
	}
	st, err := os.Stat(abs)
	if err != nil {
		return ""
	}
	if !st.IsDir() {
		abs = filepath.Dir(abs)
	}
	return abs
}

// onCloseRequested records the folder, then lets the window close. fm has
// nothing unsaved to ask about.
func onCloseRequested(c *sdk.Conn, _ uint32) bool {
	dir := closingFolder(shownDir.get())
	if dir == "" {
		return true
	}
	if err := c.SendAppMsgTo(wire.Recipient{AppID: sessionAppID}, map[string]any{
		"kind": "recent.note",
		"path": dir,
	}); err != nil {
		log.Printf("wash-fm: recent note path=%q: %v", dir, err)
		return true
	}
	log.Printf("wash-fm: recent note path=%q", dir)
	return true
}
