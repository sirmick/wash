// Taking the human to the session that wants them (docs/AGENT_UX.md
// N1+N2).
//
// agentd raises the toast for a question, and agentd resolves the click:
// it is the only party that knows whether a session already has a window,
// lost one, or never had one. The shell deliberately does not model any
// of that — it hands the key back to whoever raised the notification and
// gets out of the way (see the FOCUS_KIND contract in web/shell/src).
//
// Three outcomes for one key:
//
//   - windows are already showing it → they come forward (each one asks
//     the router to raise itself; an app may raise its own window and no
//     one else's).
//   - nothing is showing it → open one, the same spawn+attach path the
//     roster's reattach verb takes.
//   - it isn't a hosted session at all (a terminal-tier row) → nothing.
//     Those asks are toasted WITHOUT a key precisely so the desktop keeps
//     its generic fallback instead of a click that does nothing here; see
//     askKey.

package agentd

import (
	"log"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// FocusKind is the cross-app message the desktop sends back when the user
// activates a notification that named a subject key. Same string on both
// sides of the wire; the shell's copy is FOCUS_KIND in web/shell/src.
const FocusKind = "wash.focus"

type focusReq struct {
	Key string `json:"key"`
}

// askKey is the subject key a question's toast carries, or "" for a
// question the click could not be honoured for.
//
// Only hosted (ACP) sessions get one: this service can open or raise an
// Agent window for those. A terminal-tier row's window belongs to
// wash-term, which has no handler for FocusKind yet — keying those toasts
// would buy a dead click, where an unkeyed one still opens the Agent app
// with the question visible in its roster pane. Key them here the day
// wash-term learns to raise the right tab (docs/AGENT_TERM.md).
func askKey(a Ask) string {
	if lookupHosted(a.RowKey) == nil {
		return ""
	}
	return a.RowKey
}

// askToastBody is what the toast says under the title. The question
// itself, in the same words the roster row uses.
func askToastBody(a Ask) string {
	if a.Subject == "" {
		return a.Tool
	}
	return a.Tool + " " + a.Subject
}

// installAskToasts wires the ask queue's notify seam to this connection.
// Called once, from onReady.
func installAskToasts(c *sdk.Conn) {
	notifyAsk = func(a Ask) {
		title := "Agent needs you"
		if a.Agent != "" {
			title = a.Agent + " needs you"
		}
		// NotifyAbout is fire-and-forget on its own goroutine — required,
		// because this runs on the ask queue's path, which is itself on
		// the SDK dispatch path for terminal-tier asks.
		c.NotifyAbout(askKey(a), title, askToastBody(a), wire.NotifyLevelWarn)
	}
}

// registerFocusHandler installs FocusKind.
//
// HandleVoid, not HandleFromVoid: this message comes from the SHELL, which
// is not an app and so carries no router-attested sender —
// HandleFromVoid drops exactly that shape. Nothing here needs a caller
// identity anyway. The verb only opens or raises a window onto a session
// this service already holds; it starts nothing, answers nothing, and
// discloses nothing that is not already on the roster.
func registerFocusHandler(bus *sdk.Bus, _ *sdk.Conn) {
	sdk.HandleVoid(bus, FocusKind, func(conn *sdk.Conn, _ string, req focusReq) error {
		if req.Key == "" {
			return nil
		}
		focusHosted(conn, req.Key)
		return nil
	})
}

// focusHosted brings the window for one hosted session to the front,
// opening one if nothing is showing it.
func focusHosted(conn *sdk.Conn, key string) {
	if lookupHosted(key) == nil {
		log.Printf("agentd: focus key=%s: not a hosted session", key)
		return
	}
	watchers := transcriptWatchers(key)
	if len(watchers) > 0 {
		for _, instanceID := range watchers {
			_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, map[string]any{
				"kind": FocusKind,
				"key":  key,
			})
		}
		log.Printf("agentd: focus key=%s raising %d window(s)", key, len(watchers))
		return
	}
	// Nobody is rendering this session. Whether it was detached on purpose
	// or its window died without saying so, the state of the world is the
	// same and so is the fix — which is why detached is asserted here
	// rather than trusted: claimDetached is the one atomic gate that keeps
	// two clicks from becoming two windows.
	restoreDetached(key)
	h := claimDetached(key)
	if h == nil {
		return
	}
	pendingAttachMu.Lock()
	pendingAttach = append(pendingAttach, h.key)
	pendingAttachMu.Unlock()
	if err := conn.SpawnRequest(aiAppID); err != nil {
		log.Printf("agentd: focus spawn key=%s: %v", key, err)
		popAttach()
		restoreDetached(h.key)
		return
	}
	log.Printf("agentd: focus key=%s opening a window", key)
	h.republish()
}
