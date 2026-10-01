// Agent tabs (docs/AGENT_TABS.md M2): wash-edit hosts coding-agent
// sessions in the pane its terminals already live in.
//
// It is a thin host, exactly as wash-ai is — agentd owns the session, the
// transcript, the roster and the adapters — but with one difference that is
// the whole reason to do it here: the editor already knows which folder you
// are working in, so nobody has to pick one, and a tool row naming a file
// can open that file in the buffer beside the transcript.
//
// The relay is internal/agentclient rather than a second copy of wash-ai's,
// and it is KEYED because this host has several sessions at once where
// wash-ai has exactly one.

package edit

import (
	"log"
	"sync"

	"github.com/sirmick/wash/internal/agentclient"
	"github.com/sirmick/wash/internal/agentproto"
	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/pathlink"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

var (
	agentMu sync.Mutex
	agent   *agentclient.Client
	// owner is the Agent window this editor belongs to, once one has sent
	// editor.show: told when this window closes (tellOwnerClosing).
	owner string
	// ownerKey is that window's agentd session, and ownerTitle the agent's
	// own name for it. The key outlives the window — agentd reopens a closed
	// session — so it, not the instance id, is what the Agent button asks
	// agentd to raise.
	ownerKey, ownerTitle string
	// pending maps a start's req_id to the FE tab waiting for it. Without
	// it two tabs started in quick succession cannot tell whose session
	// arrived — and a FAILED start carries no key at all, so there would
	// be nothing to attribute the error to.
	pending = map[string]string{}
)

// initAgent wires the relay once the conn exists. Roster subscription is
// what makes adapter discovery and per-session state work.
func initAgent(c *sdk.Conn) {
	// Declared before New so the Started handler can call Watch on it —
	// the client is the thing the handler needs, and the handler is an
	// argument to its constructor.
	var cl *agentclient.Client
	cl = agentclient.New(c, agentclient.Handlers{
		Started: func(reqID, key, sessionID, errMsg string) {
			agentMu.Lock()
			tab := pending[reqID]
			delete(pending, reqID)
			agentMu.Unlock()
			if errMsg != "" {
				log.Printf("edit: agent start failed tab=%s: %s", tab, errMsg)
				_ = c.SendAppMsg(map[string]any{
					"kind": "agent.start_failed", "tab": tab, "error": errMsg,
				})
				return
			}
			_ = cl.Watch(key)
			_ = c.SendAppMsg(map[string]any{
				"kind": "agent.started", "tab": tab, "key": key, "session_id": sessionID,
			})
		},
		Snapshot: func(key string, events []agentproto.Event) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.snapshot", "key": key, "events": events})
		},
		Event: func(key string, event agentproto.Event) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.event", "key": key, "event": event})
		},
		State: func(state agentproto.State) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.state", "state": state})
			// The agent renames its session as the work becomes clear, so
			// the title this editor was opened with goes stale. The roster
			// it already subscribes to carries the new one.
			agentMu.Lock()
			key := ownerKey
			agentMu.Unlock()
			for _, r := range state.Rows {
				if r.Key == key && r.Title != "" {
					adoptOwner(c, key, r.Title)
				}
			}
		},
	})
	agentMu.Lock()
	agent = cl
	agentMu.Unlock()
	_ = cl.SubscribeRoster()
}

func agentClient() *agentclient.Client {
	agentMu.Lock()
	defer agentMu.Unlock()
	return agent
}

// onAgentMsgFrom routes agentd's messages into the relay. The sender is
// router-attested, so a message claiming to be the roster service is one.
func onAgentMsgFrom(data any, from wire.Sender) bool {
	if from.AppID != agentproto.AppID {
		return false
	}
	cl := agentClient()
	if cl == nil {
		return false
	}
	return cl.Handle(data)
}

type agentStartReq struct {
	// Tab is the FE's own id for the tab that asked, echoed back on
	// agent.started so the reply lands in the right one.
	Tab   string `json:"tab"`
	Agent string `json:"agent"`
	// Cwd is optional: empty means the folder the editor has open, which
	// is the point of hosting an agent here.
	Cwd string `json:"cwd,omitempty"`
}

// agentWindowAppID is the Agent window, the one app allowed to drive this
// editor with editor.show: each Agent window keeps one editor as its own.
const agentWindowAppID = "com.wash.ai"

// editorShowReq brings this window forward and, with a path, opens that
// file at the line.
type editorShowReq struct {
	// Key and Title name the Agent session this editor serves; both are
	// absent from a plain cmd.open_file, which reuses this type.
	Key   string `json:"key,omitempty"`
	Title string `json:"title,omitempty"`
	Path  string `json:"path,omitempty"`
	Line  int    `json:"line,omitempty"`
	Col   int    `json:"col,omitempty"`
}

// titleForSession is what this editor's window is called once it belongs to
// an Agent session. A taskbar of windows all called "Editor" says nothing
// about which conversation each one is for.
func titleForSession(title string) string {
	if title == "" {
		return "Editor"
	}
	return "Editor · " + title
}

// adoptOwner records the Agent session this editor serves and titles the
// window after it. Returns whether the FE needs telling.
func adoptOwner(c *sdk.Conn, key, title string) bool {
	agentMu.Lock()
	if key == "" || key == ownerKey && title == ownerTitle {
		agentMu.Unlock()
		return false
	}
	ownerKey, ownerTitle = key, title
	agentMu.Unlock()
	if err := c.SetTitle(titleForSession(title)); err != nil {
		log.Printf("edit: title for session %s: %v", key, err)
	}
	return true
}

// onAgentInstanceGone forgets an Agent window that closed, so the editor is
// claimable again. Without this the owner slot held a dead instance id for
// the life of the window: messages back went nowhere, and — now that the
// claim is exclusive — no other agent could ever take it.
func onAgentInstanceGone(instanceID string) {
	agentMu.Lock()
	defer agentMu.Unlock()
	if owner == instanceID {
		log.Printf("edit: owner %s closed", instanceID)
		owner = ""
	}
}

// tellOwnerClosing tells the Agent window that owns this editor that it is
// closing. The router's instance.gone says the same, but only once the
// process is reaped, and a file clicked in between would be sent here and
// lost; the owner opens a new editor for it instead.
func tellOwnerClosing(c *sdk.Conn) {
	agentMu.Lock()
	to := owner
	agentMu.Unlock()
	if to == "" {
		return
	}
	if err := c.SendAppMsgTo(wire.Recipient{InstanceID: to}, map[string]any{"kind": "editor.closing"}); err != nil {
		log.Printf("edit: tell owner %s closing: %v", to, err)
	}
}

// pathProbeReq asks which of an agent tab's path-shaped tokens are files
// under the folder the tab's session works in.
type pathProbeReq struct {
	Base  string   `json:"base"`
	Paths []string `json:"paths"`
}

type pathProbeReply struct {
	Hits []pathlink.Hit `json:"hits"`
}

type agentKeyReq struct {
	Key  string `json:"key"`
	Text string `json:"text,omitempty"`
	ID   string `json:"id,omitempty"`
	Mode string `json:"mode,omitempty"`
	Rule string `json:"rule,omitempty"`
	// Decision is allow | deny on an answer.
	Decision string `json:"decision,omitempty"`
	Value    string `json:"value,omitempty"`
}

// registerAgentHandlers installs the FE-facing verbs. Each mirrors one
// agentclient call; the mapping is deliberately boring.
func registerAgentHandlers(b *sdk.Bus) {
	sdk.HandleVoid(b, "agent.start", func(_ *sdk.Conn, _ string, req agentStartReq) error {
		cl := agentClient()
		if cl == nil {
			return nil
		}
		// An empty Agent is not a mistake: it means "the default catalog",
		// which agentd resolves (startProfile, docs/PLACES.md §4.5). The FE
		// only offers it when a default is set, so the no-default error is
		// not a path a click can reach.
		cwd := req.Cwd
		if cwd == "" {
			cwd = root
		}
		reqID, err := cl.Start(req.Agent, cwd, "")
		if err != nil {
			return err
		}
		agentMu.Lock()
		pending[reqID] = req.Tab
		agentMu.Unlock()
		log.Printf("edit: agent start tab=%s agent=%s cwd=%s req=%s", req.Tab, req.Agent, cwd, reqID)
		return nil
	})
	sdk.HandleVoid(b, "agent.resync", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.Resync(req.Key)
		}
		return nil
	})
	sdk.HandleVoid(b, "agent.prompt", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.Prompt(req.Key, req.Text)
		}
		return nil
	})
	sdk.HandleVoid(b, "agent.answer", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.Answer(req.ID, req.Decision, req.Rule)
		}
		return nil
	})
	sdk.HandleVoid(b, "agent.cancel", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.Cancel(req.Key)
		}
		return nil
	})
	sdk.HandleVoid(b, "agent.set_mode", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.SetMode(req.Key, req.Mode)
		}
		return nil
	})
	sdk.HandleVoid(b, "agent.set_config", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			return cl.SetConfig(req.Key, req.ID, req.Value)
		}
		return nil
	})
	// agent.close: the tab is going. The SESSION is not — agentd outlives
	// its hosts, which is what makes Resume possible — so this only stops
	// routing its events here.
	sdk.HandleVoid(b, "agent.close", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			cl.Forget(req.Key)
		}
		return nil
	})
	// editor.show comes from the Agent window this editor belongs to: a
	// file named in its transcript was clicked, or its Editor button. The
	// sender is router-attested, and the path is confined here as any other.
	sdk.HandleFromVoid(b, "editor.show", func(c *sdk.Conn, _ string, req editorShowReq, from wire.Sender) error {
		if from.AppID != agentWindowAppID {
			return nil
		}
		agentMu.Lock()
		// Exclusive: an editor already claimed by a LIVE agent window is
		// not taken over by a second one, which would leave the first
		// silently pointing at an editor that no longer answers it
		// (docs/PLACES.md §4.3). owner is cleared when that window closes
		// (onAgentInstanceGone), so the slot does free up.
		if owner != "" && owner != from.InstanceID {
			agentMu.Unlock()
			log.Printf("edit: declined editor.show from %s; owned by %s", from.InstanceID, owner)
			return nil
		}
		owner = from.InstanceID
		agentMu.Unlock()
		adoptOwner(c, req.Key, req.Title)
		if err := c.Raise(); err != nil {
			log.Printf("edit: raise for %s: %v", from.InstanceID, err)
		}
		if req.Path == "" {
			return nil
		}
		abs, err := editFS.Confine(req.Path)
		if err != nil {
			return sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return bus.Emit("cmd.open_file", editorShowReq{Path: abs, Line: req.Line, Col: req.Col})
	})
	// agent.path_probe: which tokens in an agent tab's transcript are files
	// under the session's folder, so only those become links.
	sdk.Handle(b, "agent.path_probe", func(_ *sdk.Conn, _ string, req pathProbeReq) (pathProbeReply, error) {
		base, err := editFS.Confine(req.Base)
		if err != nil {
			return pathProbeReply{}, sdk.Err{Code: wfs.ErrCode(err), Msg: err.Error()}
		}
		return pathProbeReply{Hits: pathlink.Probe(base, req.Paths)}, nil
	})
	// agent.stop ends the session for good — the other answer to "what do
	// I do with the agent when its tab closes".
	sdk.HandleVoid(b, "agent.stop", func(_ *sdk.Conn, _ string, req agentKeyReq) error {
		if cl := agentClient(); cl != nil {
			cl.Forget(req.Key)
			return cl.Stop(req.Key)
		}
		return nil
	})
}
