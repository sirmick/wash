package ai

// The Agent window's editor: the Editor in this window's Places group
// (docs/PLACES.md), rooted at the session's folder, like an IDE's editor
// beside its agent pane. The Places bar's Editor icon brings it forward
// (opening it the first time), and a file clicked in the transcript opens
// there, at its line.
//
//	FE → BE  : { kind: "path_probe", id, paths }     which tokens are files
//	           { kind: "editor_show", token? }        raise; open token's file
//	BE → FE  : { kind: "path_probe_ok", id, hits }
//	ai → edit: places.show { group, members, key, title, path?, line?, col? }
//	edit → ai: { kind: "editor.closing" }
//
// Tokens are resolved here, against the session's folder, by
// internal/pathlink — only files at or below it are links — and again when
// one is clicked, so the FE never names a path this window will open.
//
// Which editor is ours is the GROUP's: this file used to keep its own
// instance handle, spawn queue and adoption, which Places now does for all
// four apps (internal/places). Keeping both meant the Editor button and the
// Places Editor icon led to two different editors.

import (
	"log"

	"github.com/sirmick/wash/internal/pathlink"
	"github.com/sirmick/wash/internal/places"
	"github.com/sirmick/wash/pkg/sdk"
)

const editAppID = "com.wash.edit"

type pathProbeOK struct {
	Kind string         `json:"kind"`
	ID   string         `json:"id"`
	Hits []pathlink.Hit `json:"hits"`
}

// probePaths answers the FE's link probe for this window's session.
func probePaths(c *sdk.Conn, id string, paths []string) {
	_ = c.SendAppMsg(pathProbeOK{Kind: "path_probe_ok", ID: id, Hits: pathlink.Probe(session.cwd, paths)})
}

// showEditor brings the group's editor forward, opening it the first time,
// and opens the file token names when it names one. An empty token is the
// Editor icon.
func showEditor(c *sdk.Conn, token string) {
	if session.cwd == "" {
		log.Printf("wash-ai: editor: session key=%s has no folder yet", session.key)
		return
	}
	var path string
	var line, col int
	if token != "" {
		hit, ok := pathlink.Resolve(session.cwd, token)
		if !ok {
			log.Printf("wash-ai: editor: %q is not a file under %s", token, session.cwd)
			return
		}
		path, line, col = hit.Path, hit.Line, hit.Col
	}
	// The editor learns which conversation it belongs to from the group, so
	// the session must be current before the invitation goes out.
	group.SetSession(c, session.key, session.title)
	if err := group.Click(c, places.AppEditor, session.cwd, path, line, col); err != nil {
		log.Printf("wash-ai: editor: %v", err)
		_ = c.Fail("Could not open the editor", err)
	}
}
