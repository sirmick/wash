package agentd

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

func resetTranscripts() {
	transMu.Lock()
	trans = map[string]*transcript{}
	transSubs = map[string]map[string]struct{}{}
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
		if e := appendUpdate("acp:1", chunk(s), now); e == nil {
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

// The buffer is bounded, and trimming must not leave a tool index pointing
// at the wrong line — an update for a dropped row has to append, never
// corrupt an unrelated one.
func TestTranscriptIsBoundedAndReindexes(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)

	for i := 0; i < maxTranscript+50; i++ {
		appendUpdate("acp:1", acp.SessionUpdate{
			SessionUpdate: acp.UpdateToolCall,
			ToolCall:      acp.ToolCall{ToolCallID: "t" + itoa(uint64(i)), Kind: acp.ToolKindRead, Status: acp.ToolStatusPending},
		}, now)
	}
	got := snapshot("acp:1")
	if len(got) != maxTranscript {
		t.Fatalf("%d events, want the bound of %d", len(got), maxTranscript)
	}

	// An update for a long-dropped tool must not mutate a survivor.
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCallUpdate,
		ToolCall:      acp.ToolCall{ToolCallID: "t0", Status: acp.ToolStatusFailed},
	}, now)
	after := snapshot("acp:1")
	for i, e := range after[:len(after)-1] {
		if e.Status == acp.ToolStatusFailed {
			t.Fatalf("event %d was corrupted by an update for a dropped row: %+v", i, e)
		}
	}

	// A survivor's update still lands in place.
	last := got[len(got)-1].ToolID
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCallUpdate,
		ToolCall:      acp.ToolCall{ToolCallID: last, Status: acp.ToolStatusCompleted},
	}, now)
	final := snapshot("acp:1")
	var found bool
	for _, e := range final {
		if e.ToolID == last && e.Status == acp.ToolStatusCompleted {
			found = true
		}
	}
	if !found {
		t.Error("a surviving tool row lost its index after the trim")
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
	transSubs["acp:1"] = map[string]struct{}{"i-9": {}}
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
	transSubs["acp:1"] = map[string]struct{}{"i-1": {}, "i-2": {}}
	transSubs["acp:2"] = map[string]struct{}{"i-1": {}}
	transMu.Unlock()

	forgetInstanceTranscripts("i-1")

	if got := transcriptWatchers("acp:1"); len(got) != 1 || got[0] != "i-2" {
		t.Errorf("acp:1 watchers = %v, want just i-2", got)
	}
	if got := transcriptWatchers("acp:2"); len(got) != 0 {
		t.Errorf("acp:2 watchers = %v, want none", got)
	}
}
