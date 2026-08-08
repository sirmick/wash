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
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

var (
	agentMu sync.Mutex
	agent   *agentclient.Client
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
		Snapshot: func(key string, events any) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.snapshot", "key": key, "events": events})
		},
		Event: func(key string, event any) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.event", "key": key, "event": event})
		},
		State: func(state any) {
			_ = c.SendAppMsg(map[string]any{"kind": "agent.state", "state": state})
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
	if from.AppID != agentclient.AppID {
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
		if cl == nil || req.Agent == "" {
			return nil
		}
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
