package agentd

import (
	"sync"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/pkg/sdk"
)

// captureSends swaps transcriptSend for a recorder and returns it with a
// restore func. Frames are the app messages as pushEvent built them.
type sentFrame struct {
	inst string
	ev   Event
	kind string
}

func captureSends(t *testing.T) (*[]sentFrame, func() []sentFrame) {
	t.Helper()
	var mu sync.Mutex
	var got []sentFrame
	prev := transcriptSend
	transcriptSend = func(_ *sdk.Conn, inst string, msg map[string]any) {
		mu.Lock()
		defer mu.Unlock()
		ev, _ := msg["event"].(Event)
		got = append(got, sentFrame{inst: inst, ev: ev, kind: msg["kind"].(string)})
	}
	prevDelay := streamFlushDelay
	streamFlushDelay = 10 * time.Millisecond
	t.Cleanup(func() {
		transcriptSend = prev
		streamFlushDelay = prevDelay
		emitMu.Lock()
		emitters = map[string]*keyEmitter{}
		emitMu.Unlock()
	})
	return &got, func() []sentFrame {
		mu.Lock()
		defer mu.Unlock()
		return append([]sentFrame(nil), got...)
	}
}

func watch(key, inst string) {
	transMu.Lock()
	if transSubs[key] == nil {
		transSubs[key] = map[string]time.Time{}
	}
	transSubs[key][inst] = time.Now()
	transMu.Unlock()
}

func stream(key string, texts ...string) {
	for _, s := range texts {
		for _, e := range appendUpdate(key, chunk(s), time.Unix(0, 0)) {
			pushEvent(nil, key, e)
		}
	}
}

func settle() { time.Sleep(60 * time.Millisecond) }

// A streamed reply goes out as its first chunk whole, then the rest as
// deltas coalesced over the flush window — not the whole message again on
// every chunk.
func TestStreamedChunksGoOutAsCoalescedDeltas(t *testing.T) {
	resetTranscripts()
	_, frames := captureSends(t)
	watch("acp:1", "win-a")

	stream("acp:1", "Hello", ", ", "world")
	settle()

	got := frames()
	if len(got) != 2 {
		t.Fatalf("%d frames, want 2 (first chunk + one delta): %+v", len(got), got)
	}
	first, delta := got[0].ev, got[1].ev
	if first.Append || first.Text != "Hello" || first.TextLen != 5 {
		t.Errorf("first = %+v, want whole \"Hello\" text_len=5", first)
	}
	if !delta.Append || delta.Text != ", world" || delta.TextLen != 12 || delta.Seq != first.Seq {
		t.Errorf("delta = %+v, want append \", world\" text_len=12 same seq", delta)
	}

	// More text later: another delta from where the last one ended.
	stream("acp:1", "!")
	settle()
	got = frames()
	if len(got) != 3 || got[2].ev.Text != "!" || got[2].ev.TextLen != 13 {
		t.Fatalf("after more text: %+v", got)
	}
}

// An event that closes the streaming message (a tool call) flushes what
// the message had pending FIRST, so the wire order is the transcript order.
func TestClosingEventFlushesPendingDeltaFirst(t *testing.T) {
	resetTranscripts()
	_, frames := captureSends(t)
	watch("acp:1", "win-a")

	stream("acp:1", "Let me ", "look.")
	tool := acp.SessionUpdate{SessionUpdate: acp.UpdateToolCall, ToolCall: acp.ToolCall{ToolCallID: "t1", Kind: acp.ToolKindRead, Title: "read", Status: acp.ToolStatusPending}}
	for _, e := range appendUpdate("acp:1", tool, time.Unix(0, 0)) {
		pushEvent(nil, "acp:1", e)
	}
	got := frames()
	if len(got) != 3 {
		t.Fatalf("%d frames, want 3: %+v", len(got), got)
	}
	if !got[1].ev.Append || got[1].ev.Text != "look." {
		t.Errorf("frame 2 = %+v, want the pending delta", got[1].ev)
	}
	if got[2].ev.Kind != EventTool {
		t.Errorf("frame 3 = %+v, want the tool event", got[2].ev)
	}
}

// A subscriber's snapshot is taken after pending deltas flush, under the
// same lock: the base it holds is exactly what the next delta appends to.
func TestSnapshotFlushesPendingDeltasFirst(t *testing.T) {
	resetTranscripts()
	_, frames := captureSends(t)
	watch("acp:1", "win-a")

	stream("acp:1", "one ", "two")
	// A second window subscribes mid-stream. Its snapshot goes over the
	// real conn (nil here → skipped), but the flush it forces is captured.
	watch("acp:1", "win-b")
	em := emitterFor("acp:1", nil)
	em.mu.Lock()
	em.flushLockedExcept("win-b")
	snap := snapshot("acp:1")
	em.mu.Unlock()

	// The flush reaches the existing watcher only: the new one has no
	// base for a delta and is about to get the whole text in its snapshot.
	got := frames()
	if len(got) != 2 || got[1].ev.Text != "two" || got[1].inst != "win-a" {
		t.Fatalf("frames before snapshot = %+v, want first chunk + flushed delta to win-a only", got)
	}
	if len(snap) != 1 || snap[0].Text != "one two" {
		t.Fatalf("snapshot = %+v", snap)
	}
	stream("acp:1", " three")
	settle()
	got = frames()
	last := got[len(got)-1].ev
	if !last.Append || last.Text != " three" || last.TextLen != len("one two three") {
		t.Errorf("delta after snapshot = %+v, want \" three\" over the snapshot's base", last)
	}
}
