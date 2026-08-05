// Session transcripts (docs/AGENT_APP.md §9, M4).
//
// The roster answers "what are my agents doing"; a transcript answers "what
// did THIS one say". They are different subscriptions on purpose:
//
//   - The roster is one small state pushed to every subscriber. A
//     transcript is large and per-session, so pushing it through the same
//     StateService would send every session's history to the sidebar.
//   - **Transcript subscribers must not count as roster subscribers.** The
//     ask queue defers when nobody is watching (ask.go), and if opening a
//     transcript pane changed that answer, watching an agent would silently
//     change how its permissions are decided. Two counters, deliberately.
//
// The buffer lives here rather than in the app so that a reload, a second
// window, and M6's terminal pane are all just another subscriber — none of
// them owns the history.
package agentd

import (
	"log"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/wire"
)

// maxTranscript bounds one session's history. Generous enough to scroll
// back through a long turn, finite so a runaway agent cannot grow the
// service without bound. Oldest events fall off the front.
const maxTranscript = 500

// Event kinds. Deliberately fewer than ACP's update variants: the
// transcript renders messages and tool calls, and everything else
// (usage, available commands, session info) is roster or nothing.
const (
	EventMessage = "message"
	EventThought = "thought"
	EventTool    = "tool"
	// EventUser is what the human typed. ACP has a user_message_chunk
	// variant, but an agent does not echo the prompt its client just sent
	// it — so a transcript built purely from notifications shows the
	// answers with none of the questions. wash records its own side.
	EventUser = "user"
)

// Event is one line in a transcript.
//
// Flat and string-typed on purpose: this crosses the router to the FE, and
// structured byte fields get base64'd on the way (the CBOR pitfall).
type Event struct {
	Seq  uint64 `json:"seq"`
	Kind string `json:"kind"`
	// Text is the message body, accumulated across streamed chunks.
	Text string `json:"text,omitempty"`
	// Tool fields, set when Kind == EventTool.
	ToolID   string `json:"tool_id,omitempty"`
	ToolKind string `json:"tool_kind,omitempty"`
	Title    string `json:"title,omitempty"`
	Status   string `json:"status,omitempty"`
	// AtMS is wall-clock at first append, for the FE's own clock anchoring.
	AtMS int64 `json:"at_ms"`
}

type transcript struct {
	seq    uint64
	events []Event
	// toolAt indexes tool events by their ACP tool-call id, so a
	// tool_call_update mutates the row it belongs to instead of appending
	// a second one.
	toolAt map[string]int
	// openMessage is the index of the message event still accumulating
	// chunks, or -1. Streaming a sentence arrives as many chunks; six
	// transcript lines for one sentence would be unreadable.
	openMessage int
}

var (
	transMu sync.Mutex
	trans   = map[string]*transcript{}
	// transSubs maps a session key to the instances watching it. Separate
	// from the StateService's own subscriber set — see the file comment.
	transSubs = map[string]map[string]struct{}{}
)

func newTranscript() *transcript {
	return &transcript{toolAt: map[string]int{}, openMessage: -1}
}

// appendPrompt records what the human sent, so the transcript reads as a
// conversation rather than a monologue.
func appendPrompt(key, text string, now time.Time) Event {
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		t = newTranscript()
		trans[key] = t
	}
	// A prompt always closes any open agent message: the turn is over.
	t.openMessage = -1
	return t.push(Event{Kind: EventUser, Text: text, AtMS: now.UnixMilli()})
}

// appendUpdate folds one ACP notification into a session's transcript and
// returns the event to push, or nil when the update is not transcript
// material. Called on the ACP read path, so it must not block.
func appendUpdate(key string, u acp.SessionUpdate, now time.Time) *Event {
	transMu.Lock()
	defer transMu.Unlock()

	t := trans[key]
	if t == nil {
		t = newTranscript()
		trans[key] = t
	}

	switch u.SessionUpdate {
	case acp.UpdateAgentMessageChunk, acp.UpdateAgentThoughtChunk:
		kind := EventMessage
		if u.SessionUpdate == acp.UpdateAgentThoughtChunk {
			kind = EventThought
		}
		text := u.Content.String()
		if text == "" {
			return nil
		}
		// Continue the open message when it is the same kind; a tool call
		// in between closes it, because the agent has moved on.
		if t.openMessage >= 0 && t.events[t.openMessage].Kind == kind {
			t.events[t.openMessage].Text += text
			e := t.events[t.openMessage]
			return &e
		}
		e := t.push(Event{Kind: kind, Text: text, AtMS: now.UnixMilli()})
		t.openMessage = len(t.events) - 1
		return &e

	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		t.openMessage = -1
		id := u.ToolCallID
		if at, ok := t.toolAt[id]; ok && id != "" {
			// Update in place: a tool row moves pending → in_progress →
			// completed, it does not become three rows.
			ev := &t.events[at]
			if u.Title != "" {
				ev.Title = u.Title
			}
			if u.Kind != "" {
				ev.ToolKind = u.Kind
			}
			if u.Status != "" {
				ev.Status = u.Status
			}
			if txt := u.Content.String(); txt != "" {
				ev.Text = txt
			}
			e := *ev
			return &e
		}
		e := t.push(Event{
			Kind:     EventTool,
			ToolID:   id,
			ToolKind: u.Kind,
			Title:    u.Title,
			Status:   u.Status,
			Text:     u.Content.String(),
			AtMS:     now.UnixMilli(),
		})
		if id != "" {
			t.toolAt[id] = len(t.events) - 1
		}
		return &e
	}
	return nil
}

// push appends, trims to the bound, and stamps a sequence number.
func (t *transcript) push(e Event) Event {
	t.seq++
	e.Seq = t.seq
	t.events = append(t.events, e)
	if len(t.events) > maxTranscript {
		drop := len(t.events) - maxTranscript
		t.events = append([]Event(nil), t.events[drop:]...)
		// Indices shift; rebuild the ones that still exist and forget the
		// rest, so an update for a dropped tool row appends rather than
		// corrupting an unrelated line.
		t.toolAt = map[string]int{}
		for i := range t.events {
			if t.events[i].Kind == EventTool && t.events[i].ToolID != "" {
				t.toolAt[t.events[i].ToolID] = i
			}
		}
		if t.openMessage >= 0 {
			t.openMessage -= drop
			if t.openMessage < 0 {
				t.openMessage = -1
			}
		}
	}
	return e
}

// snapshot copies a session's transcript for a new subscriber.
func snapshot(key string) []Event {
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		return nil
	}
	return append([]Event(nil), t.events...)
}

// dropTranscript forgets a finished session's history and its watchers.
func dropTranscript(key string) {
	transMu.Lock()
	delete(trans, key)
	delete(transSubs, key)
	transMu.Unlock()
}

// transcriptWatchers is who to push an event to.
func transcriptWatchers(key string) []string {
	transMu.Lock()
	defer transMu.Unlock()
	subs := transSubs[key]
	if len(subs) == 0 {
		return nil
	}
	out := make([]string, 0, len(subs))
	for id := range subs {
		out = append(out, id)
	}
	return out
}

// transcriptSubscriberCount is diagnostic only. It must never be confused
// with svc.SubscriberCount(), which is what decides whether a permission
// question can be asked at all.
func transcriptSubscriberCount(key string) int {
	transMu.Lock()
	defer transMu.Unlock()
	return len(transSubs[key])
}

// pushEvent fans one event out to the windows watching that session.
func pushEvent(conn *sdk.Conn, key string, e Event) {
	for _, inst := range transcriptWatchers(key) {
		_ = conn.SendAppMsgTo(wire.Recipient{InstanceID: inst}, map[string]any{
			"kind":  "transcript_event",
			"key":   key,
			"event": e,
		})
	}
}

// registerTranscriptHandlers installs the per-session subscription verbs.
func registerTranscriptHandlers(bus *sdk.Bus) {
	sdk.HandleFromVoid(bus, "transcript_subscribe", func(conn *sdk.Conn, _ string, req transReq, from wire.Sender) error {
		if from.InstanceID == "" || req.Key == "" {
			return nil
		}
		transMu.Lock()
		if transSubs[req.Key] == nil {
			transSubs[req.Key] = map[string]struct{}{}
		}
		transSubs[req.Key][from.InstanceID] = struct{}{}
		transMu.Unlock()

		// Reply with the history so a reload, or a second window, starts
		// where the session actually is rather than empty.
		return conn.SendAppMsgTo(wire.Recipient{InstanceID: from.InstanceID}, map[string]any{
			"kind":   "transcript_snapshot",
			"key":    req.Key,
			"events": snapshot(req.Key),
		})
	})

	sdk.HandleFromVoid(bus, "transcript_unsubscribe", func(_ *sdk.Conn, _ string, req transReq, from wire.Sender) error {
		if from.InstanceID == "" {
			return nil
		}
		transMu.Lock()
		if subs := transSubs[req.Key]; subs != nil {
			delete(subs, from.InstanceID)
			if len(subs) == 0 {
				delete(transSubs, req.Key)
			}
		}
		transMu.Unlock()
		return nil
	})
}

// forgetInstanceTranscripts drops every subscription held by a window that
// went away without unsubscribing — a closed tab, a crashed browser.
func forgetInstanceTranscripts(instance string) {
	transMu.Lock()
	defer transMu.Unlock()
	for key, subs := range transSubs {
		if _, ok := subs[instance]; ok {
			delete(subs, instance)
			log.Printf("agentd: transcript unsubscribed instance=%s key=%s (gone)", instance, key)
		}
		if len(subs) == 0 {
			delete(transSubs, key)
		}
	}
}

type transReq struct {
	Key string `json:"key"`
}
