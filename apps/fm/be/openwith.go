// "Open with…" — the FE's chooser for launching a file in a specific app,
// rather than whatever the router's ext → app index picks for a
// double-click. Two requests:
//
//	FE → fm   open_with_list {path}          → {registered:[…], others:[…]}
//	FE → fm   open_with      {app_id, path}  → open_with_ok {app_id, path}
//
// The candidate list comes from the in-process app registry: in the
// shipped multicall layout every compiled-in app's manifest (and so its
// Opens) is visible here, so the chooser can rank the apps registered for
// the file's extension first. In a standalone-binary layout only fm is
// registered, so a static roster of the known file-openers (editor, image
// viewer, terminal) backs it up — the router still validates the target
// on spawn, so an absent app just fails the spawn (logged, not fatal).
//
// "Open terminal here" rides the same seam with a fixed target:
//
//	FE → fm   open_terminal {dir}            → open_terminal_ok {dir}
//
// The launch itself is SpawnRequestOpen: the router starts the chosen app
// with `--open <path>` argv (the same seam open routing uses), which needs
// fm to declare CapSpawn.
package fm

import (
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

type openWithListReq struct {
	Path string `json:"path"`
}

type openWithApp struct {
	AppID string `json:"app_id"`
	Name  string `json:"name"`
}

type openWithListResp struct {
	Path       string        `json:"path"`
	Registered []openWithApp `json:"registered"` // apps whose Opens include the extension
	Others     []openWithApp `json:"others"`     // the remaining file-openers
}

type openWithReq struct {
	AppID string `json:"app_id"`
	Path  string `json:"path"`
}

// terminalAppID is the app "Open terminal here" targets. Fixed rather
// than chosen: the point of the verb is "a shell, here".
const terminalAppID = "com.wash.term"

type openTerminalReq struct {
	Dir string `json:"dir"`
}

type openTerminalResp struct {
	Dir string `json:"dir"`
}

// knownOpeners is the fallback roster: apps that take a file (or folder)
// through `--open`, offered even when the registry can't see them.
var knownOpeners = []openWithApp{
	{AppID: "com.wash.edit", Name: "Editor"},
	{AppID: "com.wash.imageview", Name: "Image Viewer"},
	{AppID: "com.wash.term", Name: "Terminal"},
}

func registerOpenWithHandlers(b *sdk.Bus) {
	sdk.Handle(b, "open_with_list", func(_ *sdk.Conn, _ string, req openWithListReq) (openWithListResp, error) {
		abs, err := fmFS.Confine(req.Path)
		if err != nil {
			return openWithListResp{}, fsErr(err, req.Path)
		}
		reg, others := openersFor(filepath.Base(abs), registryManifests())
		return openWithListResp{Path: abs, Registered: reg, Others: others}, nil
	})
	sdk.HandleVoid(b, "open_with", func(conn *sdk.Conn, _ string, req openWithReq) error {
		if req.AppID == "" {
			return sdk.Errf(sdk.ErrBadRequest, "missing app_id")
		}
		abs, err := fmFS.Confine(req.Path)
		if err != nil {
			log.Printf("fm: open_with app=%s path=%q: %v", req.AppID, req.Path, err)
			return fsErr(err, req.Path)
		}
		log.Printf("fm: open_with app=%s path=%q", req.AppID, abs)
		return conn.SpawnRequestOpen(req.AppID, abs)
	})
	// "Open terminal here": spawn wash-term with the folder as its launch
	// path. A file's parent is used, so the verb works from a file row too.
	sdk.Handle(b, "open_terminal", func(conn *sdk.Conn, _ string, req openTerminalReq) (openTerminalResp, error) {
		abs, err := fmFS.Confine(req.Dir)
		if err != nil {
			log.Printf("fm: open_terminal dir=%q: %v", req.Dir, err)
			return openTerminalResp{}, fsErr(err, req.Dir)
		}
		if st, serr := os.Stat(abs); serr != nil {
			return openTerminalResp{}, fsErr(serr, req.Dir)
		} else if !st.IsDir() {
			abs = filepath.Dir(abs)
		}
		log.Printf("fm: open_terminal dir=%q app=%s", abs, terminalAppID)
		// TODO: once the term track lands `--open <dir>` as the first tab's
		// cwd, assert the SHELL's cwd here (e2e currently asserts the spawn
		// carried the dir + a term window appearing).
		return openTerminalResp{Dir: abs}, conn.SpawnRequestOpen(terminalAppID, abs)
	})
}

// registryManifests returns every compiled-in app manifest except fm's own.
func registryManifests() []wire.Manifest {
	var out []wire.Manifest
	for _, a := range registry.All() {
		if a.Manifest.ID == def.Manifest.ID {
			continue
		}
		out = append(out, a.Manifest)
	}
	return out
}

// openersFor splits the candidate apps for a file name into those that
// registered its extension (Opens) and the other known openers. Only
// window-surface, non-hidden apps qualify; both lists are name-sorted so
// the menu is stable. The fallback roster fills in whatever the registry
// can't see, so the chooser is never empty.
func openersFor(name string, manifests []wire.Manifest) (registered, others []openWithApp) {
	ext := strings.ToLower(filepath.Ext(name))
	seen := map[string]bool{}
	for _, m := range manifests {
		if m.Surface != wire.SurfaceWindow || m.Hidden || m.ID == "" {
			continue
		}
		app := openWithApp{AppID: m.ID, Name: m.Name}
		if ext != "" && hasExt(m.Opens, ext) {
			registered = append(registered, app)
			seen[m.ID] = true
			continue
		}
		if isKnownOpener(m.ID) {
			others = append(others, app)
			seen[m.ID] = true
		}
	}
	for _, k := range knownOpeners {
		if !seen[k.AppID] {
			others = append(others, k)
		}
	}
	byName := func(s []openWithApp) { sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name }) }
	byName(registered)
	byName(others)
	return registered, others
}

func hasExt(opens []string, ext string) bool {
	for _, o := range opens {
		o = strings.ToLower(o)
		if !strings.HasPrefix(o, ".") {
			o = "." + o
		}
		if o == ext {
			return true
		}
	}
	return false
}

func isKnownOpener(id string) bool {
	for _, k := range knownOpeners {
		if k.AppID == id {
			return true
		}
	}
	return false
}
