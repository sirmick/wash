package agentd

import (
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
	"github.com/sirmick/wash/internal/agentproto"
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
	events := []agentproto.Event{
		{Seq: 1, Kind: agentproto.EventMessage, Text: "a"},
		{Seq: 2, Kind: agentproto.EventMessage, Text: "b"},
	}
	old := maxTranscriptSnapshotBytes
	maxTranscriptSnapshotBytes = 1
	defer func() { maxTranscriptSnapshotBytes = old }()

	msgs := transcriptSnapshotMsgs("acp:1", events, "e", 0)
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
	appendEvent("acp:1", agentproto.Event{Kind: agentproto.EventMessage, Text: "kept"}, time.Unix(0, 0))
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
	if got[0].Kind != agentproto.EventImage || got[0].Mime != "image/png" || got[0].Text != "AAAA" {
		t.Errorf("image event = %+v", got[0])
	}
	if got[1].Kind != agentproto.EventMessage || got[1].Text != "and some words" {
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

	if len(got) != 1 || got[0].Kind != agentproto.EventMessage {
		t.Fatalf("events = %+v, want one note", got)
	}
	if len(got[0].Text) > 100 {
		t.Error("the oversize image was kept after all")
	}
}

// The other half of the keepalive: a watcher that idles past the TTL is
// dropped, and a keepalive that then arrives brings it BACK — as a fresh
// subscription that gets its snapshot, not as silence. This is the agentd
// side of the transcript-freeze fix; internal/agentclient's ticker is the
// host side, and the e2e proves them together.
func TestReaffirmedWatcherOutlivesTheTTL(t *testing.T) {
	resetTranscripts()
	old := watcherTTL
	watcherTTL = 30 * time.Millisecond
	t.Cleanup(func() { watcherTTL = old })

	now := time.Now()
	if !affirmWatcher("acp:1", "win-1", now) {
		t.Fatal("first subscribe was not fresh")
	}
	// A repeat inside the TTL is a keepalive: known, no snapshot.
	if affirmWatcher("acp:1", "win-1", now.Add(10*time.Millisecond)) {
		t.Fatal("a keepalive inside the TTL counted as a fresh subscription")
	}

	// Idle past the TTL without re-affirming: gone.
	time.Sleep(2 * watcherTTL)
	if got := transcriptWatchers("acp:1"); len(got) != 0 {
		t.Fatalf("silent watcher survived the TTL: %v", got)
	}

	// The keepalive lands late (a busy host, a paused laptop): the window
	// is still there, so it is a watcher again — and fresh, so the
	// subscribe handler replays the history it may have missed.
	if !affirmWatcher("acp:1", "win-1", time.Now()) {
		t.Fatal("a re-affirmation after expiry was not treated as fresh — the window would get no snapshot back")
	}
	if got := transcriptWatchers("acp:1"); len(got) != 1 || got[0] != "win-1" {
		t.Fatalf("watchers after re-affirmation = %v, want [win-1]", got)
	}

	// And a watcher that keeps affirming inside the TTL never drops out,
	// however long it idles — which is what a ticker at TTL/4 buys.
	for i := 0; i < 6; i++ {
		time.Sleep(watcherTTL / 3)
		affirmWatcher("acp:1", "win-1", time.Now())
	}
	if got := transcriptWatchers("acp:1"); len(got) != 1 {
		t.Fatalf("a re-affirming watcher was dropped: %v", got)
	}
}

// Every change stamps its row with the transcript's next rev — appends,
// streamed growth, and tool rows updated in place — so a window that says
// "I hold up to rev N" can be sent just what changed after.
func TestTranscriptRevsStampEveryChange(t *testing.T) {
	resetTranscripts()
	now := time.Unix(0, 0)
	appendPrompt("acp:1", "hi", now)
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCall,
		ToolCall:      acp.ToolCall{ToolCallID: "t1", Title: "ls", Status: acp.ToolStatusPending},
	}, now)
	appendUpdate("acp:1", chunk("Hel"), now)
	_, epoch := snapshotEpoch("acp:1")
	held := snapshot("acp:1")
	since := held[len(held)-1].Rev

	appendUpdate("acp:1", chunk("lo"), now) // grows the open message
	appendUpdate("acp:1", acp.SessionUpdate{
		SessionUpdate: acp.UpdateToolCallUpdate,
		ToolCall:      acp.ToolCall{ToolCallID: "t1", Status: acp.ToolStatusCompleted},
	}, now)

	events, cur := snapshotEpoch("acp:1")
	if cur != epoch || epoch == "" {
		t.Fatalf("epoch %q then %q, want one stable non-empty epoch", epoch, cur)
	}
	msgs := transcriptSnapshotMsgs("acp:1", events, cur, since)
	if len(msgs) != 1 || msgs[0].Reset || msgs[0].Since != since || msgs[0].Epoch != epoch {
		t.Fatalf("partial snapshot = %+v, want one non-reset frame since %d", msgs, since)
	}
	var got []string
	for _, e := range msgs[0].Events {
		got = append(got, e.Kind+":"+e.Text+e.Status)
	}
	if len(got) != 2 || got[0] != "tool:"+acp.ToolStatusCompleted || got[1] != "message:Hello" {
		t.Fatalf("changed rows = %v, want the completed tool and the grown message (in seq order)", got)
	}

	// Nothing changed since the newest rev: one empty frame, so the window
	// still gets its answer.
	events, _ = snapshotEpoch("acp:1")
	top := uint64(0)
	for _, e := range events {
		top = max(top, e.Rev)
	}
	msgs = transcriptSnapshotMsgs("acp:1", events, cur, top)
	if len(msgs) != 1 || len(msgs[0].Events) != 0 || msgs[0].Reset {
		t.Fatalf("up-to-date snapshot = %+v, want one empty non-reset frame", msgs)
	}
}

// A history replaced from disk starts a new epoch: revs a window holds from
// before mean nothing against it.
func TestTranscriptRenumberStartsNewEpoch(t *testing.T) {
	resetTranscripts()
	appendPrompt("acp:1", "hi", time.Unix(0, 0))
	_, before := snapshotEpoch("acp:1")
	transMu.Lock()
	trans["acp:1"].renumber()
	transMu.Unlock()
	events, after := snapshotEpoch("acp:1")
	if after == before {
		t.Fatal("renumber kept the epoch")
	}
	if events[0].Rev != 1 {
		t.Fatalf("rev after renumber = %d, want 1", events[0].Rev)
	}
}
