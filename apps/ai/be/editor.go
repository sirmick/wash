package ai

// The Agent window's editor: one wash-edit window per Agent window, rooted
// at the session's folder, like an IDE's editor beside its agent pane. The
// Editor button brings it forward (opening it the first time), and a file
// clicked in the transcript opens there, at its line.
//
//	FE → BE  : { kind: "path_probe", id, paths }     which tokens are files
//	           { kind: "editor_show", token? }        raise; open token's file
//	BE → FE  : { kind: "path_probe_ok", id, hits }
//	ai → edit: { kind: "editor.show", path?, line?, col? }
//	edit → ai: { kind: "editor.closing" }
//
// Tokens are resolved here, against the session's folder, by
// internal/pathlink — only files at or below it are links — and again when
// one is clicked, so the FE never names a path this window will open.
//
// Which editor is ours is the instance id the router's spawn reply named.
// It is forgotten when the editor says it is closing (or, failing that, when
// the router reports the instance gone), and the next show opens a new one.
// The spawned editor is always sent one editor.show, even for the Editor
// button, because that is how it learns which Agent window to tell.

import (
	"log"
	"sync"

	"github.com/sirmick/wash/internal/pathlink"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

const editAppID = "com.wash.edit"

type (
	pathProbeOK struct {
		Kind string         `json:"kind"`
		ID   string         `json:"id"`
		Hits []pathlink.Hit `json:"hits"`
	}
	editorShowMsg struct {
		Kind string `json:"kind"`
		Path string `json:"path,omitempty"`
		Line int    `json:"line,omitempty"`
		Col  int    `json:"col,omitempty"`
	}
)

var editor struct {
	mu       sync.Mutex
	instance string
	// spawning is true between asking the router for an editor and its
	// reply; a show in that window waits in pending rather than opening a
	// second editor. The last one wins: it is the most recent click.
	spawning bool
	pending  editorShowMsg
}

// probePaths answers the FE's link probe for this window's session.
func probePaths(c *sdk.Conn, id string, paths []string) {
	_ = c.SendAppMsg(pathProbeOK{Kind: "path_probe_ok", ID: id, Hits: pathlink.Probe(session.cwd, paths)})
}

// showEditor brings this window's editor forward, opening the file token
// names when it names one. An empty token is the Editor button.
func showEditor(c *sdk.Conn, token string) {
	if session.cwd == "" {
		log.Printf("wash-ai: editor: session key=%s has no folder yet", session.key)
		return
	}
	msg := editorShowMsg{Kind: "editor.show"}
	if token != "" {
		hit, ok := pathlink.Resolve(session.cwd, token)
		if !ok {
			log.Printf("wash-ai: editor: %q is not a file under %s", token, session.cwd)
			return
		}
		msg.Path, msg.Line, msg.Col = hit.Path, hit.Line, hit.Col
	}
	editor.mu.Lock()
	inst := editor.instance
	if inst == "" {
		editor.pending = msg
		if editor.spawning {
			editor.mu.Unlock()
			return
		}
		editor.spawning = true
	}
	editor.mu.Unlock()
	if inst != "" {
		if err := c.SendAppMsgTo(wire.Recipient{InstanceID: inst}, msg); err != nil {
			log.Printf("wash-ai: editor show instance=%s: %v", inst, err)
		}
		return
	}
	log.Printf("wash-ai: editor: opening one at %s", session.cwd)
	if err := c.SpawnRequestOpen(editAppID, session.cwd); err != nil {
		editor.mu.Lock()
		editor.spawning = false
		editor.mu.Unlock()
		log.Printf("wash-ai: editor spawn: %v", err)
		c.Fail("Could not open the editor", err)
	}
}

// onSpawnResult adopts the editor the router started for showEditor, and
// hands it what was clicked while it was starting. The editor reads its
// launch folder before it reads messages, so the file lands in a window
// already rooted at the session's folder.
func onSpawnResult(c *sdk.Conn, appID, instanceID string, err error) {
	if appID != editAppID {
		return
	}
	editor.mu.Lock()
	if !editor.spawning {
		editor.mu.Unlock()
		return
	}
	editor.spawning = false
	msg := editor.pending
	editor.pending = editorShowMsg{}
	if err == nil {
		editor.instance = instanceID
	}
	editor.mu.Unlock()
	if err != nil {
		log.Printf("wash-ai: editor spawn: %v", err)
		c.Fail("Could not open the editor", err)
		return
	}
	log.Printf("wash-ai: editor instance=%s for key=%s", instanceID, session.key)
	if err := c.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, msg); err != nil {
		log.Printf("wash-ai: editor show instance=%s: %v", instanceID, err)
	}
}

// onInstanceGone forgets an editor that closed, so the next show opens
// another rather than talking to nobody. Also the editor's own
// editor.closing, which arrives first.
func onInstanceGone(_ *sdk.Conn, _ string, instanceID string) {
	editor.mu.Lock()
	defer editor.mu.Unlock()
	if editor.instance == instanceID {
		log.Printf("wash-ai: editor instance=%s closed", instanceID)
		editor.instance = ""
	}
}
