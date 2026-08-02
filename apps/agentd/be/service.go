package agentd

import (
	"log"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// Liveness (docs/AGENT_TERM.md §7). A terminal re-states each of its
// agents periodically, so silence means the producer is gone: the row
// greys out, then disappears. Generous enough to ride out a busy box,
// short enough that a killed window doesn't leave a roster full of
// history.
const (
	staleAfter = 60 * time.Second
	dropAfter  = 2 * time.Minute
	sweepEvery = 10 * time.Second
)

// gitCacheTTL bounds how often we shell git for one directory. Rows
// refresh at least every 15s and there may be several in one repo; the
// branch does not change that fast.
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

// rows is the live roster, keyed by "<term instance>:<channel id>".
var rows = map[string]*row{}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-agentd ready instance=%s", instanceID)
	bus := sdk.NewBus(c)
	loadHistory()
	svc = sdk.NewStateService(bus, State{Recent: publishHistory()})

	// agent_status: a terminal states (or re-states) one tab's agent. The
	// sender is router-attested, so the key can't be forged and a
	// terminal can only ever describe its own tabs.
	sdk.HandleFromVoid(bus, "agent_status", func(_ *sdk.Conn, _ string, req statusReq, from wire.Sender) error {
		if from.InstanceID == "" || req.ChannelID == 0 {
			return nil
		}
		now := time.Now()
		key := rowKey(from.InstanceID, req.ChannelID)
		var wantGit string
		var changedState bool
		svc.Mutate(func(s *State) {
			r := rows[key]
			if r == nil {
				r = &row{}
				rows[key] = r
			}
			// A state change restarts the elapsed clock; a keepalive for
			// the same state must not.
			if r.State != req.State || r.Reason != req.Reason {
				r.stateSince = now.Add(-time.Duration(req.SinceMS) * time.Millisecond)
				changedState = true
			}
			r.lastSeen = now
			r.Stale = false
			r.Key = key
			r.Agent = req.Agent
			r.State = req.State
			r.Reason = req.Reason
			r.SessionID = req.SessionID
			r.TermInstance = from.InstanceID
			r.WindowID = req.WindowID
			r.ChannelID = req.ChannelID
			if req.Cwd != "" && req.Cwd != r.Cwd {
				// New directory: show it immediately, resolve git after.
				r.Cwd = req.Cwd
				r.Dir = dirLabel(req.Cwd)
				r.Branch, r.Dirty = "", false
			}
			if r.Cwd != "" {
				wantGit = r.Cwd
			}
			// Remember it too: the roster is "now", the history is "what
			// I lost" (§13).
			if rememberSession(req.Agent, req.SessionID, req.Cwd, now) {
				historyDirty = true
			}
			s.Rows = publish(now)
			s.Recent = publishHistory()
		})
		if changedState {
			// One line per roster transition: the roster is also the audit
			// surface for "what were my agents doing at 3am".
			log.Printf("agentd: row key=%s agent=%s state=%s session=%s dir=%s",
				key, req.Agent, req.State, req.SessionID, dirLabel(req.Cwd))
		}
		if wantGit != "" {
			// Off the dispatch path: shelling git must never delay the
			// next inbound message.
			go resolveGit(wantGit)
		}
		return nil
	})

	// agent_gone: the tab closed (or its agent ended). An explicit
	// goodbye is the fast path; the sweep is the safety net.
	sdk.HandleFromVoid(bus, "agent_gone", func(_ *sdk.Conn, _ string, req goneReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		key := rowKey(from.InstanceID, req.ChannelID)
		svc.Mutate(func(s *State) {
			delete(rows, key)
			s.Rows = publish(time.Now())
			// The row is gone but the session is now exactly what the
			// Recent list is for.
			s.Recent = publishHistory()
		})
		saveHistory()
		return nil
	})

	// agent_resume: a Resume/Fork click in the sidebar (§13).
	sdk.HandleFromVoid(bus, "agent_resume", func(conn *sdk.Conn, _ string, req resumeReq, _ wire.Sender) error {
		if req.SessionID == "" {
			return nil
		}
		resumeSession(conn, req.SessionID, req.Fork)
		return nil
	})

	registerAskHandlers(bus, c)

	go sweepLoop(c)
}

// sweepLoop ages rows out: a terminal that died without saying goodbye
// leaves a row that greys and then goes. Runs until the conn closes.
func sweepLoop(c *sdk.Conn) {
	t := time.NewTicker(sweepEvery)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			now := time.Now()
			svc.Mutate(func(s *State) {
				changed := false
				for key, r := range rows {
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
				if !changed {
					// Republish anyway only if something moved; a quiet
					// roster stays off the wire.
					return
				}
				s.Rows = publish(now)
			})
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
	case "done":
		return 3
	}
	return 4 // stale, and anything a newer terminal invents
}

func rowKey(instance string, channelID uint64) string {
	return instance + ":" + itoa(channelID)
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

type statusReq struct {
	ChannelID uint64 `json:"channel_id"`
	WindowID  uint64 `json:"window_id"`
	Agent     string `json:"agent"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	SinceMS   int64  `json:"since_ms"`
}

type goneReq struct {
	ChannelID uint64 `json:"channel_id"`
}

type resumeReq struct {
	SessionID string `json:"session_id"`
	Fork      bool   `json:"fork"`
}
