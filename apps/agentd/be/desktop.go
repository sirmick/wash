package agentd

import (
	"log"
	"sync"

	"github.com/sirmick/wash/internal/agentproto"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// The desktop: what agentd asks of the desktop a person is using — open a
// window on a session, show a notification — as typed events
// (agentproto.OpenSession, agentproto.Notify) through one handler. Nothing
// else in agentd spawns a window or posts a notification.

// desktop handles one desktop event. A var so tests can watch the events
// without a router; Wash's implementation is washDesktop.
var desktop = washDesktop

// washDesktop is Wash's desktop: an Agent window (com.wash.ai) for a
// session, and the router's notification service for the rest, which shows
// a question even when no Agent window is open.
func washDesktop(c *sdk.Conn, ev any) {
	if c == nil {
		return
	}
	switch e := ev.(type) {
	case agentproto.OpenSession:
		pendingAttachMu.Lock()
		pendingAttach = append(pendingAttach, e.Key)
		pendingAttachMu.Unlock()
		if err := c.SpawnRequest(aiAppID); err != nil {
			log.Printf("agentd: open controller key=%s: %v", e.Key, err)
			removePendingAttach(e.Key)
			clearControllerLaunch(e.Key)
			restoreDetached(e.Key)
			return
		}
		log.Printf("agentd: opening controller key=%s", e.Key)
	case agentproto.Notify:
		// Fire-and-forget on its own goroutine: callers include the ask
		// queue, which must not block.
		c.NotifyAbout(e.Key, e.Title, e.Body, e.Level)
	default:
		log.Printf("agentd: unknown desktop event %T", ev)
	}
}

// aiAppID is the window a reopened session appears in. Resume used to
// open a TERMINAL running `claude --resume` — which, once the intercept
// tier was deleted, produced an agent wash could no longer see at all
// (docs/AGENT_APP.md §10).
const aiAppID = "com.wash.ai"

var (
	pendingAttachMu sync.Mutex
	pendingAttach   []string
)

// popAttach takes the oldest queued attach. Spawn replies arrive in the
// order they were requested, and a click is a rare event.
func popAttach() (string, bool) {
	pendingAttachMu.Lock()
	defer pendingAttachMu.Unlock()
	if len(pendingAttach) == 0 {
		return "", false
	}
	k := pendingAttach[0]
	pendingAttach = pendingAttach[1:]
	return k, true
}

func removePendingAttach(key string) {
	pendingAttachMu.Lock()
	defer pendingAttachMu.Unlock()
	for i, pending := range pendingAttach {
		if pending == key {
			pendingAttach = append(pendingAttach[:i], pendingAttach[i+1:]...)
			return
		}
	}
}

// onSpawnResult fires when the router has started the window a resume
// asked for; it is then told which live session to attach to.
func onSpawnResult(c *sdk.Conn, appID, instanceID string, err error) {
	if appID != aiAppID {
		return
	}
	key, ok := popAttach()
	if !ok {
		return
	}
	if err != nil {
		log.Printf("agentd: resume spawn failed: %v", err)
		clearControllerLaunch(key)
		restoreDetached(key)
		return
	}
	if owner, ok := claimController(key, instanceID); !ok {
		clearControllerLaunch(key)
		if owner == "" {
			// The window died before it could be told its session: leave
			// the session detached, so the roster offers to open it again.
			log.Printf("agentd: controller instance=%s gone before attach key=%s", instanceID, key)
			restoreDetached(key)
		}
		return
	}
	if e := agentproto.Send(c, wire.Recipient{InstanceID: instanceID}, agentproto.Attach{Key: key}); e != nil {
		log.Printf("agentd: resume attach instance=%s: %v", instanceID, e)
		releaseController(instanceID)
		restoreDetached(key)
	}
}
