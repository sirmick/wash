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
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// Transcripts are intentionally unbounded in agentd memory for the router's
// lifetime: if the viewer or session app crashes, the hosted agent keeps
// running and a later window should be able to replay everything agentd saw.
//
// Snapshot replay is still chunked below so one reattach cannot exceed the
// wire's per-frame limit.
var maxTranscriptSnapshotBytes = wire.MaxPayload / 4

// Watcher liveness. The router sends instance.gone when it tears down an app,
// and the TTL remains as a backstop for any older or missed lifecycle path.
// Watchers therefore re-affirm, exactly as the roster's own rows do, and one
// that goes quiet is dropped.
const (
	watcherTTL     = 60 * time.Second
	WatcherRefresh = 15 * time.Second
)

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
	// EventTerminal is a command the agent handed to wash to run
	// (acpterm.go). Channel carries the raw channel its pty writes to, so
	// a transcript can mount a live terminal on it — the difference
	// between watching the command and reading about it afterwards.
	EventTerminal = "terminal"
	// EventImage is one image the agent showed. Its own event rather than
	// a field on a message, so it renders in the order it arrived without
	// restructuring everything else.
	EventImage = "image"
)

// maxImageBytes bounds one inline image. The transcript is held in memory
// and pushed over the router, so a multi-megabyte screenshot would cost
// both — and an image too big to show is better dropped with a note than
// silently wedging the session.
const maxImageBytes = 2 << 20

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
	// Mime is set on EventImage; Text then holds the base64 bytes.
	Mime string `json:"mime,omitempty"`
	// Channel is set on EventTerminal: the raw channel id to render.
	Channel uint32 `json:"channel,omitempty"`
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

// transcriptDebug logs every streamed chunk with %q. Off unless
// WASH_AGENT_DEBUG is set — this is one line per chunk, which is many
// lines per sentence.
var transcriptDebug = os.Getenv("WASH_AGENT_DEBUG") != ""

var (
	transMu sync.Mutex
	trans   = map[string]*transcript{}
	// transSubs maps a session key to the instances watching it, and when
	// each was last heard from. Separate from the StateService's own
	// subscriber set — see the file comment.
	transSubs = map[string]map[string]time.Time{}
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

// appendEvent stores an event wash itself originated — a terminal it
// opened, a note about its own behaviour — and returns it stamped with a
// sequence number.
//
// Distinct from appendPrompt, which stores what the HUMAN typed and is what
// these callers used to borrow. Borrowing it meant the stored event kept
// Kind=user while only the pushed copy carried the real kind, so a live
// window showed one thing and a reloaded one showed another. That is
// invisible for a note and destructive for a terminal, whose channel id
// would be lost on reload.
func appendEvent(key string, e Event, now time.Time) Event {
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		t = newTranscript()
		trans[key] = t
	}
	// Anything wash says closes an open agent message, for the same reason
	// a prompt does: the thread of that message is over.
	t.openMessage = -1
	if e.AtMS == 0 {
		e.AtMS = now.UnixMilli()
	}
	return t.push(e)
}

// updateEvent rewrites a stored event in place and returns it, so a caller
// can push the result. Both hosts replace by seq, so an updated event lands
// where the original was rather than appearing twice.
func updateEvent(key string, seq uint64, mutate func(*Event)) (Event, bool) {
	transMu.Lock()
	defer transMu.Unlock()
	t := trans[key]
	if t == nil {
		return Event{}, false
	}
	for i := range t.events {
		if t.events[i].Seq == seq {
			mutate(&t.events[i])
			return t.events[i], true
		}
	}
	// Fell off the front of the bounded history: nothing to update, and
	// nothing to say about it.
	return Event{}, false
}

// appendUpdate folds one ACP notification into a session's transcript and
// returns the events to push, in order. Called on the ACP read path, so
// it must not block.
//
// Plural because one notification can produce several lines: a content
// block list carrying both an image and text is one update and two
// transcript entries. Returning a single event silently dropped the
// image from the LIVE push while still storing it, so it appeared only
// after a reload — the kind of bug that looks like a rendering problem.
func appendUpdate(key string, u acp.SessionUpdate, now time.Time) []Event {
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
		// Images arrive alongside text in the same content block list.
		// They are pushed as their own events, in order.
		var out []Event
		for _, img := range u.Content.Images() {
			if len(img.Data) > maxImageBytes {
				log.Printf("agentd: image dropped key=%s mime=%s bytes=%d (over %d)", key, img.MimeType, len(img.Data), maxImageBytes)
				out = append(out, t.push(Event{Kind: EventMessage, Text: "[image too large to show]", AtMS: now.UnixMilli()}))
				continue
			}
			t.openMessage = -1
			out = append(out, t.push(Event{Kind: EventImage, Mime: img.MimeType, Text: img.Data, AtMS: now.UnixMilli()}))
		}
		text := u.Content.String()
		if text == "" {
			return out
		}
		if transcriptDebug && len(u.Content) > 0 {
			log.Printf("agentd: content key=%s kinds=%v", key, u.Content.Kinds())
		}
		if transcriptDebug {
			// %q so a chunk that is exactly " " is visible as such. Chunk
			// boundaries are precisely where lost whitespace hides.
			log.Printf("agentd: chunk key=%s kind=%s text=%q", key, kind, text)
		}
		// Continue the open message when it is the same kind; a tool call
		// in between closes it, because the agent has moved on.
		if t.openMessage >= 0 && t.events[t.openMessage].Kind == kind {
			t.events[t.openMessage].Text += text
			return append(out, t.events[t.openMessage])
		}
		e := t.push(Event{Kind: kind, Text: text, AtMS: now.UnixMilli()})
		t.openMessage = len(t.events) - 1
		return append(out, e)

	case acp.UpdateToolCall, acp.UpdateToolCallUpdate:
		t.openMessage = -1
		// A tool can produce an image too — a screenshot, a chart. Its
		// content is nested one level deeper than a message's, which is
		// why Images() unwraps.
		var imgs []Event
		for _, img := range u.Content.Images() {
			if len(img.Data) > maxImageBytes {
				log.Printf("agentd: image dropped key=%s mime=%s bytes=%d (over %d)", key, img.MimeType, len(img.Data), maxImageBytes)
				continue
			}
			imgs = append(imgs, t.push(Event{Kind: EventImage, Mime: img.MimeType, Text: img.Data, AtMS: now.UnixMilli()}))
		}
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
			return append(imgs, *ev)
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
		return append(imgs, e)
	}
	return nil
}

// push appends and stamps a sequence number.
func (t *transcript) push(e Event) Event {
	t.seq++
	e.Seq = t.seq
	t.events = append(t.events, e)
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

type transcriptSnapshotMsg struct {
	Kind   string  `json:"kind"`
	Key    string  `json:"key"`
	Reset  bool    `json:"reset"`
	Events []Event `json:"events"`
}

func sendTranscriptSnapshot(conn *sdk.Conn, instanceID, key string) error {
	for _, msg := range transcriptSnapshotMsgs(key, snapshot(key)) {
		if err := conn.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, msg); err != nil {
			return err
		}
	}
	return nil
}

func transcriptSnapshotMsgs(key string, events []Event) []transcriptSnapshotMsg {
	if len(events) == 0 {
		return []transcriptSnapshotMsg{{Kind: "transcript_snapshot", Key: key, Reset: true}}
	}
	var out []transcriptSnapshotMsg
	var batch []Event
	var bytes int
	reset := true
	flush := func() {
		if len(batch) == 0 {
			return
		}
		out = append(out, transcriptSnapshotMsg{
			Kind:   "transcript_snapshot",
			Key:    key,
			Reset:  reset,
			Events: append([]Event(nil), batch...),
		})
		reset = false
		batch = batch[:0]
		bytes = 0
	}
	for _, e := range events {
		n := encodedEventSize(e)
		if len(batch) > 0 && bytes+n > maxTranscriptSnapshotBytes {
			flush()
		}
		batch = append(batch, e)
		bytes += n
	}
	flush()
	return out
}

func encodedEventSize(e Event) int {
	b, err := json.Marshal(e)
	if err != nil {
		return len(e.Text) + 256
	}
	return len(b) + 64
}

// forgetTranscriptWatchers drops live subscriptions for a session without
// deleting the buffered transcript. Ending or detaching a viewer is not data
// loss; a later reattach replays from trans.
func forgetTranscriptWatchers(key string) {
	transMu.Lock()
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
	now := time.Now()
	out := make([]string, 0, len(subs))
	for id, seen := range subs {
		if now.Sub(seen) > watcherTTL {
			delete(subs, id)
			log.Printf("agentd: transcript watcher expired instance=%s key=%s", id, key)
			continue
		}
		out = append(out, id)
	}
	if len(subs) == 0 {
		delete(transSubs, key)
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
//
// Liveness is the watcher's job, not this function's: SendAppMsgTo's error
// path is the local transport, not per-recipient delivery. Router
// instance.gone and transcriptWatchers expire stale recipients.
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
			transSubs[req.Key] = map[string]time.Time{}
		}
		fresh := transSubs[req.Key][from.InstanceID].IsZero()
		transSubs[req.Key][from.InstanceID] = time.Now()
		transMu.Unlock()
		if !fresh {
			// A keepalive from a window that already has the history.
			// Re-sending a whole transcript every 15s would be absurd.
			return nil
		}

		// Reply with the history so a reload, or a second window, starts
		// where the session actually is rather than empty. The history is
		// unbounded server-side, so replay is chunked into bounded frames.
		return sendTranscriptSnapshot(conn, from.InstanceID, req.Key)
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
