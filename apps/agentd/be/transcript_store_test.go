package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/acp"
)

// withStateDir points the store at a throwaway XDG_STATE_HOME and clears
// the process-global binding table, so tests don't inherit each other's
// sessions. The writer goroutine is deliberately NOT restarted — it reads
// the path per record, so a new state dir just changes where it writes.
func withStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	storeMu.Lock()
	storeSession = map[string]string{}
	storeMu.Unlock()
	transMu.Lock()
	trans = map[string]*transcript{}
	transMu.Unlock()
	t.Cleanup(func() {
		waitForTranscriptWrites()
		storeMu.Lock()
		storeSession = map[string]string{}
		storeMu.Unlock()
		transMu.Lock()
		trans = map[string]*transcript{}
		transMu.Unlock()
	})
	return dir
}

func TestTranscriptPersistsAndLoadsBack(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-a", "codex", "/tmp", now)

	appendPrompt("acp:1", "hello", now)
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "hi back"}, now)
	waitForTranscriptWrites()

	got, err := loadTranscript("sess-a")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != EventUser || got[0].Text != "hello" {
		t.Errorf("first event = %+v", got[0])
	}
	if got[1].Kind != EventMessage || got[1].Text != "hi back" {
		t.Errorf("second event = %+v", got[1])
	}
	// Seq must survive: it is what an update folds against.
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Errorf("seqs = %d,%d want 1,2", got[0].Seq, got[1].Seq)
	}
}

// A tool row moves pending → completed by rewriting one event. On disk
// that is a second line with the same seq, and loading must fold to the
// LAST one — otherwise a reloaded transcript shows work as still pending
// that finished hours ago.
func TestTranscriptUpdateFoldsToLastWrite(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-b", "codex", "/tmp", now)

	e := appendEvent("acp:1", Event{Kind: EventTool, ToolID: "t1", Title: "read", Status: "pending"}, now)
	if _, ok := updateEvent("acp:1", e.Seq, func(ev *Event) { ev.Status = "completed" }); !ok {
		t.Fatal("updateEvent did not find the event")
	}
	waitForTranscriptWrites()

	got, err := loadTranscript("sess-b")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 folded event, got %d: %+v", len(got), got)
	}
	if got[0].Status != "completed" {
		t.Errorf("status = %q, want completed (the fold kept the first write)", got[0].Status)
	}
}

// An unknown session is an empty history, not an error: a session from
// before persistence existed must not break the caller.
func TestLoadTranscriptUnknownSessionIsEmpty(t *testing.T) {
	withStateDir(t)
	got, err := loadTranscript("never-existed")
	if err != nil {
		t.Fatalf("want nil error, got %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want no events, got %d", len(got))
	}
}

// Session ids come off adapter payloads. One containing a path must not
// be able to choose where the file lands.
func TestTranscriptPathCannotEscape(t *testing.T) {
	dir := withStateDir(t)
	base := filepath.Join(dir, "wash", transcriptDirNM)
	for _, id := range []string{"../../escape", "a/b/c", "..", ".", "/etc/passwd"} {
		p := transcriptPath(id)
		if p == "" {
			t.Errorf("%q: empty path", id)
			continue
		}
		if filepath.Dir(p) != base {
			t.Errorf("%q escaped to %s", id, p)
		}
		if name := filepath.Base(p); strings.HasPrefix(name, ".jsonl") || name == ".jsonl" {
			t.Errorf("%q produced a hidden/empty name %q", id, name)
		}
	}
}

// The richer record wins. When the adapter replays less than we stored —
// including replaying nothing, which some adapters do — our own file is
// what the window should show, and new events must number past it rather
// than colliding with history.
func TestReconcileResumeKeepsTheRicherRecord(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)

	// First run: three events under the original key.
	bindTranscript("acp:1", "sess-c", "codex", "/tmp", now)
	appendPrompt("acp:1", "one", now)
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "two"}, now)
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "three"}, now)
	waitForTranscriptWrites()

	// Resume: a new roster key, and an adapter that replayed nothing.
	reconcileResume("acp:2", "sess-c", now)

	transMu.Lock()
	got := append([]Event(nil), trans["acp:2"].events...)
	seq := trans["acp:2"].seq
	transMu.Unlock()
	if len(got) != 3 {
		t.Fatalf("want the stored 3 events back, got %d: %+v", len(got), got)
	}
	if got[0].Text != "one" || got[2].Text != "three" {
		t.Errorf("wrong events came back: %+v", got)
	}
	// The counter must continue past history, or the next event would
	// overwrite one of these on the next load.
	if seq != 3 {
		t.Errorf("seq = %d, want 3 so new events number 4+", seq)
	}

	// A new event lands after the restored history, in memory and on disk.
	appendEvent("acp:2", Event{Kind: EventMessage, Text: "four"}, now)
	waitForTranscriptWrites()
	back, err := loadTranscript("sess-c")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(back) != 4 {
		t.Fatalf("want 4 events after resume+append, got %d: %+v", len(back), back)
	}
	if back[3].Text != "four" || back[3].Seq != 4 {
		t.Errorf("last event = %+v, want text=four seq=4", back[3])
	}
}

// When the adapter replays at least as much as we stored, ITS version is
// authoritative — and the file must be rewritten to match, not appended
// to, or the two numbering schemes interleave.
func TestReconcileResumePrefersAFullReplay(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)

	bindTranscript("acp:1", "sess-d", "codex", "/tmp", now)
	appendPrompt("acp:1", "old", now)
	waitForTranscriptWrites()

	// The replay lands under the new key before reconcile runs.
	bindTranscript("acp:2", "sess-d", "codex", "/tmp", now)
	appendPrompt("acp:2", "replayed one", now)
	appendEvent("acp:2", Event{Kind: EventMessage, Text: "replayed two"}, now)
	reconcileResume("acp:2", "sess-d", now)
	waitForTranscriptWrites()

	got, err := loadTranscript("sess-d")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Exactly the replay: the rewrite replaced the file rather than
	// appending the replay onto the old copy.
	if len(got) != 2 {
		t.Fatalf("want the 2 replayed events, got %d: %+v", len(got), got)
	}
	if got[0].Text != "replayed one" || got[1].Text != "replayed two" {
		t.Errorf("wrong events: %+v", got)
	}
}

// A truncated final line is what a crash mid-write leaves behind. It must
// cost that line, not the conversation.
func TestLoadTranscriptSurvivesATruncatedLine(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-e", "codex", "/tmp", now)
	appendPrompt("acp:1", "kept", now)
	waitForTranscriptWrites()

	p := transcriptPath("sess-e")
	f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	f.WriteString(`{"seq":9,"kind":"mess`)
	f.Close()

	got, err := loadTranscript("sess-e")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(got) != 1 || got[0].Text != "kept" {
		t.Fatalf("the good event was lost with the bad line: %+v", got)
	}
}

// The file is everything the human typed and everything the agent read
// back. On a shared box that is nobody else's business.
func TestTranscriptFileIsPrivate(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-f", "codex", "/tmp", now)
	appendPrompt("acp:1", "secret", now)
	waitForTranscriptWrites()

	fi, err := os.Stat(transcriptPath("sess-f"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := fi.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("mode = %v, want no group/other access", mode)
	}
	di, err := os.Stat(filepath.Dir(transcriptPath("sess-f")))
	if err != nil {
		t.Fatal(err)
	}
	if mode := di.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("dir mode = %v, want no group/other access", mode)
	}
}

// The index reads each id from the meta line rather than the file name,
// because the name is sanitised for the filesystem and may not round-trip.
func TestListSessionMetaReadsTheRealID(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "has/slash", "codex", "/tmp", now)
	appendPrompt("acp:1", "x", now)
	waitForTranscriptWrites()

	got := listSessionMeta()
	if len(got) != 1 || got[0].SessionID != "has/slash" {
		t.Fatalf("listSessionMeta ids = %+v, want [has/slash]", got)
	}
}

// Retiring a session used to leave its events in `trans` forever —
// nothing ever deleted from that map, so a long-lived router accumulated
// every transcript it had seen. Now the events live on disk and the
// in-memory copy can go, with snapshot() reading them back on demand.
func TestReleaseTranscriptFreesMemoryButKeepsTheConversation(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-g", "codex", "/tmp", now)
	appendPrompt("acp:1", "still here", now)
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "and this"}, now)

	releaseTranscript("acp:1")

	transMu.Lock()
	_, inMemory := trans["acp:1"]
	transMu.Unlock()
	if inMemory {
		t.Error("the transcript is still held in memory after release")
	}

	// A window opening on the retired session still gets the whole thing.
	got := snapshot("acp:1")
	if len(got) != 2 {
		t.Fatalf("snapshot after release = %d events, want 2: %+v", len(got), got)
	}
	if got[0].Text != "still here" || got[1].Text != "and this" {
		t.Errorf("wrong events came back: %+v", got)
	}
}

// A key that was never bound has no session and no file; snapshot must
// say "nothing" rather than reading someone else's transcript.
func TestSnapshotUnknownKeyIsEmpty(t *testing.T) {
	withStateDir(t)
	if got := snapshot("acp:never"); len(got) != 0 {
		t.Fatalf("want no events, got %d", len(got))
	}
}

// A streamed reply arrives as many chunks that grow ONE event in place
// rather than pushing new ones. Persisting only on push stored the first
// chunk and silently dropped the rest of the sentence — caught by the
// e2e, which read a real adapter's reply back off disk and found only
// its opening words.
func TestStreamedMessagePersistsEveryChunk(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-h", "codex", "/tmp", now)

	for _, chunk := range []string{"Hello ", "from ", "the fake agent."} {
		appendUpdate("acp:1", acp.SessionUpdate{
			SessionUpdate: acp.UpdateAgentMessageChunk,
			Content:       acp.Content{{Type: "text", Text: chunk}},
		}, now)
	}
	waitForTranscriptWrites()

	got, err := loadTranscript("sess-h")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	// Three chunks are ONE transcript line, folded by seq.
	if len(got) != 1 {
		t.Fatalf("want 1 folded message, got %d: %+v", len(got), got)
	}
	if got[0].Text != "Hello from the fake agent." {
		t.Errorf("text = %q, want the whole sentence", got[0].Text)
	}
}

// The index is what the history panel lists, so it must be assembled
// from a file's head and tail — never by reading the conversation. These
// pin the fields the panel needs and the two ways they arrive.
func TestSessionMetaCarriesModelAndEnding(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-m", "codex", "/home/mick/wash", now)
	// Start-of-session summary: model known, ending not.
	writeSummary("sess-m", transcriptSummary{
		Agent: "codex", Model: "Claude Opus 4.5", Cwd: "/home/mick/wash", AtMS: now.UnixMilli(),
	})
	appendPrompt("acp:1", "do the thing", now)
	// End-of-session summary: adds the title, count and ending, and must
	// not erase the model the earlier record carried.
	writeSummary("sess-m", transcriptSummary{
		Title: "Do the thing", Events: 1, EndedMS: now.Add(time.Minute).UnixMilli(),
		EndReason: "ended", AtMS: now.Add(time.Minute).UnixMilli(),
	})
	waitForTranscriptWrites()

	all := listSessionMeta()
	if len(all) != 1 {
		t.Fatalf("want 1 session, got %d", len(all))
	}
	m := all[0]
	if m.SessionID != "sess-m" {
		t.Errorf("session_id = %q", m.SessionID)
	}
	if m.Model != "Claude Opus 4.5" {
		t.Errorf("model = %q — the later summary erased it", m.Model)
	}
	if m.Title != "Do the thing" {
		t.Errorf("title = %q", m.Title)
	}
	if m.Agent != "codex" {
		t.Errorf("agent = %q", m.Agent)
	}
	if m.Dir == "" {
		t.Errorf("dir is empty; the panel needs somewhere to say where it ran")
	}
	if m.Events != 1 {
		t.Errorf("events = %d, want 1", m.Events)
	}
	if m.EndReason != "ended" || m.EndedMS == 0 {
		t.Errorf("ending = %q/%d", m.EndReason, m.EndedMS)
	}
	if m.Bytes <= 0 {
		t.Errorf("bytes = %d; history cannot offer to prune what it cannot size", m.Bytes)
	}
}

// A session the router outlived never gets a final summary. It must still
// appear, still name its model, and still sort by when it was last active
// rather than stacking at the epoch.
func TestSessionMetaSurvivesASessionThatNeverEnded(t *testing.T) {
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "sess-crash", "codex", "/tmp", now)
	writeSummary("sess-crash", transcriptSummary{Agent: "codex", Model: "gpt-5", AtMS: now.UnixMilli()})
	appendEvent("acp:1", Event{Kind: EventMessage, Text: "mid-sentence"}, now.Add(5*time.Minute))
	waitForTranscriptWrites()

	all := listSessionMeta()
	if len(all) != 1 {
		t.Fatalf("want 1 session, got %d", len(all))
	}
	m := all[0]
	if m.Model != "gpt-5" {
		t.Errorf("model = %q, want the one recorded at start", m.Model)
	}
	if m.EndReason != "" {
		t.Errorf("end_reason = %q, want empty — it never ended", m.EndReason)
	}
	// Dated by its last event, not left at zero.
	if want := now.Add(5 * time.Minute).UnixMilli(); m.EndedMS != want {
		t.Errorf("last activity = %d, want %d (the last event)", m.EndedMS, want)
	}
}

// History lists newest-first, mixing ended and never-ended sessions.
func TestListSessionMetaSortsByRecency(t *testing.T) {
	withStateDir(t)
	base := time.Unix(1_700_000_000, 0)
	for i, id := range []string{"old", "newest", "middle"} {
		bindTranscript("acp:"+id, id, "codex", "/tmp", base)
		at := base.Add(time.Duration(map[int]int{0: 1, 1: 30, 2: 10}[i]) * time.Minute)
		appendEvent("acp:"+id, Event{Kind: EventMessage, Text: id}, at)
	}
	waitForTranscriptWrites()

	got := listSessionMeta()
	if len(got) != 3 {
		t.Fatalf("want 3, got %d", len(got))
	}
	if got[0].SessionID != "newest" || got[2].SessionID != "old" {
		t.Errorf("order = %s,%s,%s want newest,middle,old",
			got[0].SessionID, got[1].SessionID, got[2].SessionID)
	}
}

// The model comes off the agent's own settings block, where the id is
// spelled differently by different adapters — and history should show the
// readable name, not the wire value.
func TestModelNameReadsTheSettingsBlock(t *testing.T) {
	cases := []struct {
		name string
		in   []acp.ConfigOption
		want string
	}{
		{"exact id, readable name", []acp.ConfigOption{{
			ID: "model", CurrentValue: "claude-opus-4-5",
			Options: []acp.ConfigOptionValue{{Value: "claude-opus-4-5", Name: "Claude Opus 4.5"}},
		}}, "Claude Opus 4.5"},
		{"no options list falls back to the value", []acp.ConfigOption{
			{ID: "model", CurrentValue: "gpt-5"},
		}, "gpt-5"},
		{"prefixed id still matches", []acp.ConfigOption{
			{ID: "openai.model", CurrentValue: "o4"},
		}, "o4"},
		{"unrelated settings only", []acp.ConfigOption{
			{ID: "reasoning_effort", CurrentValue: "high"},
		}, ""},
		{"none at all", nil, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := &hosted{key: "acp:1", configs: c.in}
			if got := h.modelName(); got != c.want {
				t.Errorf("modelName() = %q, want %q", got, c.want)
			}
		})
	}
}
