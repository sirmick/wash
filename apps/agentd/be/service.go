package agentd

import (
	"fmt"
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Liveness for rows whose session is no longer hosted: an exited session's
// row greys out, then disappears, so the roster does not fill with history
// (which the History panel shows instead).
const (
	staleAfter = 60 * time.Second
	dropAfter  = 2 * time.Minute
	sweepEvery = 10 * time.Second
)

// gitCacheTTL bounds how often the maintenance sweep shells git for one
// directory. Tool completion invalidates the cache immediately; otherwise
// the 10-second sweep observes each checkout at most once every 30 seconds.
const gitCacheTTL = 30 * time.Second

var svc *sdk.StateService[State]

// row is the internal record: the public Row plus the bookkeeping the
// sweep needs. Only ever touched inside svc.Mutate, which serializes it.
type row struct {
	Row
	lastSeen time.Time
	// stateSince is when the row entered its current state, kept as an
	// absolute so SinceMS can be recomputed on every push.
	stateSince time.Time
}

// rows is the live roster, keyed by hosted session key.
var rows = map[string]*row{}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-agentd ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	loadHistory()
	svc = sdk.NewStateService(bus, State{Recent: publishHistory(), Adapters: Probe(), HasDefaultPrompt: loadDefaultPrompt() != ""})
	controllerConn = c

	// agent_resume: a Resume/Fork click in the sidebar (§13).
	sdk.HandleFromVoid(bus, "agent_resume", func(conn *sdk.Conn, _ string, req resumeReq, _ wire.Sender) error {
		if req.SessionID == "" {
			return nil
		}
		resumeSession(conn, req.SessionID, req.Fork)
		return nil
	})

	// agent_history: the History panel's list. Answers to the asker
	// rather than broadcasting, because a history query is one window's
	// question and its results are large — pushing them through the
	// roster's StateService would send every session's metadata to the
	// sidebar on every keystroke.
	sdk.HandleFromVoid(bus, "agent_history", func(conn *sdk.Conn, _ string, req historyReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		limit := req.Limit
		if limit <= 0 || limit > historyQueryCap {
			limit = historyQueryCap
		}
		sessions := historyQuery(req.Query, limit)
		if sessions == nil {
			sessions = []SessionMeta{}
		}
		// Stamp liveness from the roster. Snapshot, not Mutate: this is a
		// read, and Mutate would push the whole state to every subscriber
		// on somebody's keystroke.
		idx := rosterIndex(svc.Snapshot().Rows)
		for i := range sessions {
			st := idx[sessions[i].SessionID]
			sessions[i].Live, sessions[i].Detached, sessions[i].RowKey = st.Live, st.Detached, st.RowKey
		}
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind":     "history",
			"query":    req.Query,
			"sessions": sessions,
		})
	})

	installAskToasts(c)
	registerFocusHandler(bus, c)
	registerAskHandlers(bus, c)
	registerACPHandlers(bus, c)
	registerSessionAdminHandlers(bus)
	registerTranscriptHandlers(bus)
	registerControllerHandlers(bus)
	if err := startWorkspaces(c, bus); err != nil {
		log.Printf("agentd: workspace service unavailable: %v", err)
	}
	// Child-spawning services group-kill on SIGTERM AND on connection
	// close; the SDK fires this hook on both.
	sdk.OnTerminate(stopAllHosted)

	go sweepLoop(c)
}

func onInstanceGone(_ *sdk.Conn, _ string, instanceID string) {
	if instanceID == "" {
		return
	}
	if svc != nil {
		svc.ForgetSubscriber(instanceID)
	}
	forgetInstanceTranscripts(instanceID)
	forgetManager(instanceID)
	noteInstanceGone(instanceID, time.Now())
	if key := releaseController(instanceID); key != "" {
		detachLostController(key)
	}
}

// sweepLoop ages out rows whose session is no longer hosted: they grey,
// then go. Runs until the conn closes.
func sweepLoop(c *sdk.Conn) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			now := time.Now()
			var wantHold string
			gitDirs := map[string]struct{}{}
			mutateStateIf(func(s *State) bool {
				changed := false
				for key, r := range rows {
					if r.Cwd != "" {
						gitDirs[r.Cwd] = struct{}{}
					}
					// A live hosted session is never aged out, however
					// idle: observed, `row dropped key=acp:1 agent=claude
					// age=2m0s` while the session was still running.
					if lookupHosted(key) != nil {
						continue
					}
					switch age := now.Sub(r.lastSeen); {
					case age > dropAfter:
						log.Printf("agentd: row dropped key=%s agent=%s age=%s", key, r.Agent, age.Round(time.Second))
						delete(rows, key)
						changed = true
					case age > staleAfter && !r.Stale:
						r.Stale = true
						changed = true
					}
				}
				if refreshDefaultPrompt(s) {
					changed = true
				}
				// Computed on every tick, not only when a row moved: the
				// needs-input ceiling expires with the clock, not with a
				// state change, so a hold has to be able to lapse on its
				// own.
				wantHold = holdReason(now)
				if !changed {
					return false
				}
				s.Rows = publish(now)
				return true
			})
			// Outside Mutate: the router is a different lock than the
			// state service, and nothing good comes of holding one while
			// waiting on the other.
			applyIdleHold(c, wantHold)
			// Git context is maintenance, not narration. resolveGit's cache
			// limits actual commands to once per TTL per checkout.
			for cwd := range gitDirs {
				go resolveGit(cwd)
			}
		}
	}
}

// publish renders the sorted public row list. Called inside Mutate.
//
// Copy-on-write: StateService.Snapshot is a shallow copy, so the slice
// handed out must be freshly built rather than mutated in place (the
// race-gate rule from [[wash race gate]]).
func publish(now time.Time) []Row {
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		pub := r.Row
		pub.SinceMS = elapsedMS(r.stateSince, now)
		if pub.Stale {
			pub.State = "stale"
		}
		out = append(out, pub)
	}
	sortRows(out)
	return out
}

// sortRows puts the roster in attention order: needs-input first (someone
// is blocked), then working, then idle-ish, then stale. Ties break on the
// longest-waiting first, so the agent that has been stuck for five
// minutes outranks the one that just asked.
func sortRows(out []Row) {
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := statePriority(out[i].State), statePriority(out[j].State)
		if pi != pj {
			return pi < pj
		}
		if out[i].SinceMS != out[j].SinceMS {
			return out[i].SinceMS > out[j].SinceMS
		}
		return out[i].Key < out[j].Key
	})
}

func statePriority(state string) int {
	switch state {
	case "needs-input":
		return 0
	case "working":
		return 1
	case "running":
		return 2
	case "failed":
		// Above done: a session that ended badly is the one ended session
		// worth looking at.
		return 3
	case "done":
		return 4
	}
	return 5 // stale, and anything else
}

func itoa(v uint64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}

// dirLabel is the short name for a working directory: its basename, which
// is the repo name in every case that matters.
func dirLabel(cwd string) string {
	clean := strings.TrimRight(cwd, "/")
	if clean == "" {
		return "/"
	}
	return path.Base(clean)
}

func elapsedMS(since, now time.Time) int64 {
	if since.IsZero() {
		return 0
	}
	ms := now.Sub(since).Milliseconds()
	if ms < 0 {
		return 0
	}
	return ms
}

type resumeReq struct {
	SessionID string `json:"session_id"`
	Fork      bool   `json:"fork"`
}

// historyQueryCap bounds one history answer. A panel shows a page at a
// time, and an unbounded reply would be one frame carrying every session
// the machine has ever run.
const historyQueryCap = 200

type historyReq struct {
	Query string `json:"query,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

// needsInputHold caps how long a row blocked on a human keeps the whole
// session alive.
//
// needs-input cuts both ways, which is why it needs a number rather than
// a rule. It is the state you MOST want to survive a disconnect — the
// agent is blocked on a person who is by definition absent — and also the
// one state that could pin a session forever, because nothing about it
// resolves on its own. Twelve hours survives the overnight disconnect
// this was written for and still lets a forgotten session go.
const needsInputHold = 12 * time.Hour

// lastHold is the reason most recently sent to the router. The inhibit is
// level-triggered, so re-sending is harmless, but the router logs every
// hold and release — sending only on change keeps that log readable.
var lastHold string

// holdReason returns why this session must not be reaped for idleness, or
// "" if it may be. Called inside Mutate.
//
// An agent mid-turn is the whole point: the router's idea of idle is
// "no browser attached", which is precisely the moment an agent's work is
// least interruptible and most likely to be lost.
func holdReason(now time.Time) string {
	working, waiting := 0, 0
	for _, r := range rows {
		switch r.State {
		case "working":
			working++
		case "needs-input":
			if now.Sub(r.stateSince) < needsInputHold {
				waiting++
			}
		}
	}
	switch {
	case working > 0 && waiting > 0:
		return fmt.Sprintf("%d agent(s) working, %d waiting on you", working, waiting)
	case working > 0:
		return fmt.Sprintf("%d agent(s) working", working)
	case waiting > 0:
		return fmt.Sprintf("%d agent(s) waiting on you", waiting)
	}
	return ""
}

// applyIdleHold pushes a changed hold to the router.
func applyIdleHold(c *sdk.Conn, reason string) {
	if reason == lastHold {
		return
	}
	lastHold = reason
	if reason == "" {
		log.Printf("agentd: idle hold released — no agent needs this session kept alive")
		_ = c.IdleInhibit(false, "")
		return
	}
	log.Printf("agentd: idle hold %q — this session will not be reaped while it stands", reason)
	_ = c.IdleInhibit(true, reason)
}
