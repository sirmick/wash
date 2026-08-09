// Package agentclient is the host side of the agentd protocol: the handful
// of messages an app sends to com.wash.agentd to run a coding-agent session,
// and the routing of what agentd sends back.
//
// agentd owns sessions, transcripts, the roster and the ACP adapters. A host
// owns a window and a UI. That split is why wash-ai describes itself as "a
// thin host" — and why the relay is worth having once rather than per app:
// wash-edit's agent tabs (docs/AGENT_TABS.md) are the second host, and every
// fix to a hand-rolled relay would otherwise land in one and not the other.
//
// The one real difference from wash-ai's original code is that everything
// here is KEYED. wash-ai is one session per process, so it compares an
// arriving key against a package-level variable; an editor hosting a strip of
// tabs has several live at once and must fan events to the right one. A host
// with a single session simply registers one.
package agentclient

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// AppID is agentd's app id — the recipient of everything sent here.
const AppID = "com.wash.agentd"

// Handlers is what a host wants to be told. Every callback is optional; a nil
// one drops its message rather than panicking, so a host can adopt the parts
// it needs. All are called on the conn's read goroutine.
type Handlers struct {
	// Started reports the outcome of Start, matched by the req_id Start
	// returned. err is non-empty when the adapter refused to launch, in
	// which case key is empty — which is exactly why the id exists.
	Started func(reqID, key, sessionID, err string)
	// Snapshot is the whole transcript for a session, sent on subscribe.
	Snapshot func(key string, events any)
	// Event is one transcript event appended to a session.
	Event func(key string, event any)
	// State is the roster push (adapters, rows, per-session status). Not
	// keyed — it describes every session agentd knows about.
	State func(state any)
}

// Client relays to agentd over a host app's conn.
type Client struct {
	conn *sdk.Conn
	h    Handlers

	mu   sync.RWMutex
	keys map[string]bool // sessions this host is subscribed to

	seq atomic.Uint64
}

// New builds a client. h may be zero — a host that only sends is legal.
func New(c *sdk.Conn, h Handlers) *Client {
	return &Client{conn: c, h: h, keys: map[string]bool{}}
}

func (cl *Client) send(m map[string]any) error {
	return cl.conn.SendAppMsgTo(wire.Recipient{AppID: AppID}, m)
}

// SubscribeRoster asks agentd for roster pushes (adapters + session rows).
func (cl *Client) SubscribeRoster() error {
	return cl.send(map[string]any{"kind": sdk.StateServiceKindSubscribe})
}

// Start launches an adapter in cwd and returns the request id that will come
// back on Handlers.Started. prompt may be empty (an idle session).
//
// The id is minted here rather than taken from the caller so two hosts, or
// two tabs, cannot collide on the same one.
func (cl *Client) Start(agent, cwd, prompt string) (reqID string, err error) {
	reqID = fmt.Sprintf("s%d", cl.seq.Add(1))
	return reqID, cl.send(map[string]any{
		"kind":   "agent_start",
		"agent":  agent,
		"cwd":    cwd,
		"prompt": prompt,
		"req_id": reqID,
	})
}

// Resume reopens a session agentd has on disk; it arrives back as an attach.
func (cl *Client) Resume(sessionID string) error {
	return cl.send(map[string]any{"kind": "agent_resume", "session_id": sessionID})
}

// Watch registers a session key so Snapshot/Event for it reach this host, and
// subscribes to its transcript. Called for a session this host just started
// and for one it is attaching to.
func (cl *Client) Watch(key string) error {
	if key == "" {
		return nil
	}
	cl.mu.Lock()
	cl.keys[key] = true
	cl.mu.Unlock()
	return cl.send(map[string]any{"kind": "transcript_subscribe", "key": key})
}

// Forget stops routing a session's events here. The session itself is
// untouched — agentd outlives its hosts, which is the whole point of Resume.
func (cl *Client) Forget(key string) {
	cl.mu.Lock()
	delete(cl.keys, key)
	cl.mu.Unlock()
}

// Watching reports whether key is one of this host's sessions.
func (cl *Client) Watching(key string) bool {
	cl.mu.RLock()
	defer cl.mu.RUnlock()
	return cl.keys[key]
}

// Prompt sends another turn to a live session.
func (cl *Client) Prompt(key, text string) error {
	if key == "" {
		return nil
	}
	return cl.send(map[string]any{"kind": "agent_prompt", "key": key, "text": text})
}

// Answer resolves a pending permission question. A non-empty rule means the
// user chose "always", which agentd persists as a standing decision.
func (cl *Client) Answer(askID, decision, rule string) error {
	return cl.send(map[string]any{
		"kind":     "agent_answer",
		"id":       askID,
		"decision": decision,
		"remember": rule != "",
		"rule":     rule,
	})
}

// Cancel aborts the running turn, leaving the session alive.
func (cl *Client) Cancel(key string) error {
	if key == "" {
		return nil
	}
	return cl.send(map[string]any{"kind": "agent_cancel", "key": key})
}

// SetMode switches the agent's approval preset.
func (cl *Client) SetMode(key, modeID string) error {
	if key == "" {
		return nil
	}
	return cl.send(map[string]any{"kind": "agent_set_mode", "key": key, "mode": modeID})
}

// SetConfig changes one of the agent's own settings.
func (cl *Client) SetConfig(key, id, value string) error {
	if key == "" {
		return nil
	}
	return cl.send(map[string]any{"kind": "agent_set_config", "key": key, "id": id, "value": value})
}

// Stop ends a session for good.
func (cl *Client) Stop(key string) error {
	if key == "" {
		return nil
	}
	return cl.send(map[string]any{"kind": "agent_stop", "key": key})
}

// Handle routes one message from agentd. It reports whether the message was
// one of agentd's own — a host uses that to fall through to its other
// senders. The caller MUST have checked that the sender is agentd (the router
// attests it); this only decides what the payload means.
//
// A transcript message for a key this host is not watching is dropped rather
// than delivered: several hosts can watch different sessions on one agentd,
// and a stray event must not paint someone else's transcript.
func (cl *Client) Handle(data any) bool {
	m, _ := data.(map[string]any)
	if m == nil {
		return false
	}
	switch str(m["kind"]) {
	case "agent_started":
		if cl.h.Started != nil {
			cl.h.Started(str(m["req_id"]), str(m["key"]), str(m["session_id"]), str(m["error"]))
		}
		return true
	case "attach":
		// A resumed session: agentd hands back the key it loaded. Same
		// shape as a start, minus the request that asked for it.
		if cl.h.Started != nil {
			cl.h.Started("", str(m["key"]), str(m["session_id"]), "")
		}
		return true
	case "transcript_snapshot":
		key := str(m["key"])
		if cl.Watching(key) && cl.h.Snapshot != nil {
			cl.h.Snapshot(key, m["events"])
		}
		return true
	case "transcript_event":
		key := str(m["key"])
		if cl.Watching(key) && cl.h.Event != nil {
			cl.h.Event(key, m["event"])
		}
		return true
	case sdk.StateServiceKindState:
		if cl.h.State != nil {
			cl.h.State(m["state"])
		}
		return true
	}
	return false
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
