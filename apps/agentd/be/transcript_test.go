package agentd

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

func resetTranscripts() {
	transMu.Lock()
	trans = map[string]*transcript{}
	transSubs = map[string]map[string]time.Time{}
	transMu.Unlock()
}

func chunk(text string) acp.SessionUpdate {
	return acp.SessionUpdate{SessionUpdate: acp.UpdateAgentMessageChunk, Content: acp.Content{{Type: "text", Text: text}}}
}

// A streamed sentence arrives as many chunks. Six transcript lines for one
// sentence would be unreadable, so consecutive chunks of the same kind
// accumulate into one event.
func TestMessageChunksCoalesce(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	for _, s := range []string{"Hello", ", ", "world"} {
		if e := appendUpdate("acp:1", chunk(s), now); len(e) == 0 {
			t.Fatal("a message chunk produced no event")
		}
	}

	got := snapshot("acp:1")
	if len(got) != 1 {
		t.Fatalf("%d events, want 1 — chunks did not coalesce: %+v", len(got), got)
	}
	if got[0].Text != "Hello, world" {
		t.Errorf("text = %q, want %q", got[0].Text, "Hello, world")
	}
}

// A tool row moves pending → in_progress → completed. It must not become
// three rows, and a later update must find the row it belongs to.
func TestToolCallUpdatesInPlace(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCall,
		ToolCall:      acp.ToolCall{ToolCallID: "t1", Kind: acp.ToolKindExecute, Title: "git status", Status: acp.ToolStatusPending},
	}, now)
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCallUpdate,
		ToolCall:      acp.ToolCall{ToolCallID: "t1", Status: acp.ToolStatusCompleted},
	}, now)

	got := snapshot("acp:1")
	if len(got) != 1 {
		t.Fatalf("%d events, want 1 — the update appended instead of mutating: %+v", len(got), got)
	}
	if got[0].Status != acp.ToolStatusCompleted {
		t.Errorf("status = %q, want completed", got[0].Status)
	}
	// The update carried no title; the original must survive it.
	if got[0].Title != "git status" {
		t.Errorf("title = %q — an update with an absent field cleared it", got[0].Title)
	}
}

// A tool call between messages ends the open one: the agent has moved on,
// and its next sentence is a new line.
func TestToolCallClosesTheOpenMessage(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	appendUpdate("acp:1", chunk("before"), now)
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCall,
		ToolCall:      acp.ToolCall{ToolCallID: "t1", Kind: acp.ToolKindRead},
	}, now)
	appendUpdate("acp:1", chunk("after"), now)

	got := snapshot("acp:1")
	if len(got) != 3 {
		t.Fatalf("%d events, want 3: %+v", len(got), got)
	}
	if got[2].Text != "after" {
		t.Errorf("post-tool message = %q — it merged into the pre-tool one", got[2].Text)
	}
}

func TestTranscriptIsUnboundedInMemory(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	const total = 600
	for i := 0; i < total; i++ {
		appendUpdate("acp:1", acp.SessionUpdate{
			SessionUpdate: acp.UpdateToolCall,
			ToolCall:      acp.ToolCall{ToolCallID: "t" + itoa(uint64(i)), Kind: acp.ToolKindRead, Status: acp.ToolStatusPending},
		}, now)
	}
	got := snapshot("acp:1")
	if len(got) != total {
		t.Fatalf("%d events, want all %d", len(got), total)
	}
	if got[0].Seq != 1 || got[len(got)-1].Seq != total {
		t.Fatalf("seq range = %d..%d, want 1..%d", got[0].Seq, got[len(got)-1].Seq, total)
	}

	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCallUpdate,
		ToolCall:      acp.ToolCall{ToolCallID: "t0", Status: acp.ToolStatusFailed},
	}, now)
	after := snapshot("acp:1")
	if len(after) != total || after[0].Status != acp.ToolStatusFailed {
		t.Fatalf("oldest event was not retained and updated in place: len=%d first=%+v", len(after), after[0])
	}
}

func TestTranscriptSnapshotReplayIsChunked(t *testing.T) {
	resetTranscripts()
	events := []Event{
		{Seq: 1, Kind: EventMessage, Text: "a"},
		{Seq: 2, Kind: EventMessage, Text: "b"},
	}
	old := maxTranscriptSnapshotBytes
	maxTranscriptSnapshotBytes = 1
	defer func() { maxTranscriptSnapshotBytes = old }()

	msgs := transcriptSnapshotMsgs("acp:1", events)
	if len(msgs) != len(events) {
		t.Fatalf("%d snapshot chunks, want %d", len(msgs), len(events))
	}
	if !msgs[0].Reset {
		t.Fatal("first snapshot chunk must reset the client")
	}
	if msgs[1].Reset {
		t.Fatal("later snapshot chunks must append")
	}
}

func TestForgetTranscriptWatchersKeepsHistory(t *testing.T) {
	resetTranscripts()
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "kept"}, time.Unix(0, 0))
	transMu.Lock()
	transSubs["acp:1"] = map[string]time.Time{"i-1": time.Now()}
	transMu.Unlock()

	forgetTranscriptWatchers("acp:1")

	if got := transcriptSubscriberCount("acp:1"); got != 0 {
		t.Fatalf("watchers=%d, want 0", got)
	}
	if got := snapshot("acp:1"); len(got) != 1 || got[0].Text != "kept" {
		t.Fatalf("history = %+v, want retained transcript", got)
	}
}

// The rule the design turns on: watching a transcript must not change how
// permissions are decided. If these counters were one, opening a pane
// would silently flip an agent from "asks you" to "asks you", or worse,
// closing the last pane would defer a live question.
func TestTranscriptSubscribersAreNotRosterSubscribers(t *testing.T) {
	resetTranscripts()
	withState(t, 0) // no roster subscribers: nobody home

	transMu.Lock()
	transSubs["acp:1"] = map[string]time.Time{"i-9": time.Now()}
	transMu.Unlock()

	if transcriptSubscriberCount("acp:1") != 1 {
		t.Fatal("transcript subscriber not recorded")
	}
	if stateSubscribers() != 0 {
		t.Fatal("a transcript subscriber leaked into the roster's count — approvals would change behaviour when a pane opens")
	}

	// And with nobody home, a question still defers rather than waiting on
	// someone who is only watching the transcript.
	resetAsks()
	var got string
	enqueueAsk(askSpec{Agent: "codex", Tool: "Bash", RowKey: "acp:1"}, func(d, _ string) error { got = d; return nil })
	if got != DecisionDefer {
		t.Errorf("decision = %q, want defer", got)
	}
}

// A window that goes away without unsubscribing must not keep receiving.
func TestForgetInstanceDropsItsSubscriptions(t *testing.T) {
	resetTranscripts()
	transMu.Lock()
	transSubs["acp:1"] = map[string]time.Time{"i-1": time.Now(), "i-2": time.Now()}
	transSubs["acp:2"] = map[string]time.Time{"i-1": time.Now()}
	transMu.Unlock()

	forgetInstanceTranscripts("i-1")

	if got := transcriptWatchers("acp:1"); len(got) != 1 || got[0] != "i-2" {
		t.Errorf("acp:1 watchers = %v, want just i-2", got)
	}
	if got := transcriptWatchers("acp:2"); len(got) != 0 {
		t.Errorf("acp:2 watchers = %v, want none", got)
	}
}

// A watcher that stops re-affirming is dropped. Router instance.gone is the
// fast path, but the TTL remains a backstop for any missed lifecycle path.
func TestSilentWatchersExpire(t *testing.T) {
	resetTranscripts()
	transMu.Lock()
	transSubs["acp:1"] = map[string]time.Time{
		"fresh": time.Now(),
		"gone":  time.Now().Add(-watcherTTL - time.Second),
	}
	transMu.Unlock()

	got := transcriptWatchers("acp:1")
	if len(got) != 1 || got[0] != "fresh" {
		t.Fatalf("watchers = %v, want just the one still re-affirming", got)
	}
	if transcriptSubscriberCount("acp:1") != 1 {
		t.Error("the expired watcher was returned but not removed — it would be re-checked forever")
	}
}

// One notification can be several transcript lines: a content block list
// carrying an image and text is one update and two entries. Returning a
// single event stored the image but never pushed it, so it appeared only
// after a reload — which looks exactly like a rendering bug.
func TestImageAndTextFromOneUpdate(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	got := appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateAgentMessageChunk,
		Content: acp.Content{
			{Type: "image", MimeType: "image/png", Data: "AAAA"},
			{Type: "text", Text: "and some words"},
		},
	}, now)

	if len(got) != 2 {
		t.Fatalf("%d events, want 2 (the image AND the text): %+v", len(got), got)
	}
	if got[0].Kind != EventImage || got[0].Mime != "image/png" || got[0].Text != "AAAA" {
		t.Errorf("image event = %+v", got[0])
	}
	if got[1].Kind != EventMessage || got[1].Text != "and some words" {
		t.Errorf("text event = %+v", got[1])
	}
}

// An image too large to show is dropped with a note rather than held in
// memory and pushed over the router.
func TestOversizeImageIsDropped(t *testing.T) {
	resetTranscripts()
	big := make([]byte, maxImageBytes+1)
	for i := range big {
		big[i] = 'A'
	}
	got := appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateAgentMessageChunk,
		Content:       acp.Content{{Type: "image", MimeType: "image/png", Data: string(big)}},
	}, time.Unix(0, 0))

	if len(got) != 1 || got[0].Kind != EventMessage {
		t.Fatalf("events = %+v, want one note", got)
	}
	if len(got[0].Text) > 100 {
		t.Error("the oversize image was kept after all")
	}
}
