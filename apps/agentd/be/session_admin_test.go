package agentd

import (
	"os"
	"testing"
	"time"
)

// A person's name for a session wins everywhere the title is shown — the
// history entry the Recent menu reads, the transcript the History panel
// reads, and the roster row a live session publishes — and clearing it
// falls back to the agent's own name rather than to nothing.
func TestRenameWinsEverywhereAndClearsBack(t *testing.T) {
	withStateDir(t)
	withState(t, 1)
	resetHistory()
	reset()
	t.Cleanup(resetHistory)

	rememberSession("codex", "s-1", "/w", "Agent's own title", t0)
	bindTranscript("acp:1", "s-1", "codex", "/w", t0)
	waitForTranscriptWrites()

	if _, err := renameSession("", "s-1", "Mine", t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitForTranscriptWrites()

	if history[0].UserTitle != "Mine" || history[0].Title != "Agent's own title" {
		t.Errorf("history entry = %+v, want the user title beside the agent's", history[0])
	}
	if got := publishHistory()[0].Title; got != "Mine" {
		t.Errorf("Recent shows %q, want the user's name", got)
	}
	m, ok := readSessionMeta(transcriptPath("s-1"))
	if !ok || m.Title != "Mine" || m.UserTitle != "Mine" {
		t.Errorf("transcript meta = %+v, want title=user_title=Mine", m)
	}
	if got, _ := resolveResumeTarget("s-1"); got.UserTitle != "Mine" {
		t.Errorf("resume target lost the user title: %+v", got)
	}

	// Clearing: the agent's name comes back, in both stores.
	if _, err := renameSession("", "s-1", "", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	waitForTranscriptWrites()
	if got := publishHistory()[0].Title; got != "Agent's own title" {
		t.Errorf("after clearing, Recent shows %q", got)
	}
	m, _ = readSessionMeta(transcriptPath("s-1"))
	if m.UserTitle != "" {
		t.Errorf("after clearing, meta still carries user_title=%q", m.UserTitle)
	}
	if m.Title != "" {
		// The agent's title reaches the file through noteSession, which
		// this test never ran; what matters is that the cleared name did
		// not leave a stale "Mine" behind.
		t.Errorf("after clearing, meta title = %q", m.Title)
	}
}

// A live session renamed by key republishes its row under the new name,
// and its final summary carries it, so the name survives the session.
func TestRenameLiveSessionRepublishesRow(t *testing.T) {
	withStateDir(t)
	withState(t, 1)
	resetHistory()
	reset()
	t.Cleanup(resetHistory)

	h := &hosted{key: "acp:7", agent: "codex", sessionID: "s-7", cwd: "/w", title: "From the agent"}
	h.register()
	t.Cleanup(func() { h.retire() })
	bindTranscript(h.key, h.sessionID, "codex", "/w", t0)

	if r := waitRow(t, h.key, func(r Row) bool { return r.Title == "From the agent" }); r.Title != "From the agent" {
		t.Fatalf("row before rename: %+v", r)
	}
	sid, err := renameSession(h.key, "", "Renamed by hand", t0)
	if err != nil || sid != "s-7" {
		t.Fatalf("rename: sid=%q err=%v", sid, err)
	}
	waitRow(t, h.key, func(r Row) bool { return r.Title == "Renamed by hand" })

	// The end-of-session summary restates it.
	h.noteSession("ended", t0.Add(time.Hour))
	waitForTranscriptWrites()
	m, _ := readSessionMeta(transcriptPath("s-7"))
	if m.Title != "Renamed by hand" || m.UserTitle != "Renamed by hand" {
		t.Errorf("final meta = %+v, want the user's name", m)
	}
}

// Delete removes the file and the entry; a live session is refused.
func TestDeleteStoredSessionRemovesFileAndEntry(t *testing.T) {
	withStateDir(t)
	withState(t, 1)
	resetHistory()
	reset()
	t.Cleanup(resetHistory)

	rememberSession("codex", "s-gone", "/w", "", t0)
	bindTranscript("acp:9", "s-gone", "codex", "/w", t0)
	appendPrompt("acp:9", "hello", t0)
	waitForTranscriptWrites()
	path := transcriptPath("s-gone")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("transcript not on disk before delete: %v", err)
	}

	if err := deleteStoredSession("s-gone"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("transcript still on disk after delete: %v", err)
	}
	if len(history) != 0 {
		t.Errorf("history still lists it: %+v", history)
	}
	if err := deleteStoredSession("s-gone"); err != nil {
		t.Errorf("deleting twice should be a no-op, got %v", err)
	}

	// Live: refused, file kept.
	h := &hosted{key: "acp:10", agent: "codex", sessionID: "s-live", cwd: "/w"}
	h.register()
	t.Cleanup(func() { h.retire() })
	bindTranscript(h.key, "s-live", "codex", "/w", t0)
	waitForTranscriptWrites()
	if err := deleteStoredSession("s-live"); err == nil {
		t.Error("a live session was deleted")
	}
	if _, err := os.Stat(transcriptPath("s-live")); err != nil {
		t.Errorf("the live session's file went: %v", err)
	}
}

// Prune deletes by age and never touches a running session; max age 0
// means every finished session.
func TestPruneStoredSessionsByAge(t *testing.T) {
	withStateDir(t)
	withState(t, 1)
	resetHistory()
	reset()
	t.Cleanup(resetHistory)

	now := time.Now()
	mk := func(key, sid string, at time.Time) {
		bindTranscript(key, sid, "codex", "/w", at)
		writeSummary(sid, transcriptSummary{EndedMS: at.UnixMilli(), EndReason: "ended", AtMS: at.UnixMilli()})
	}
	mk("acp:a", "old", now.Add(-48*time.Hour))
	mk("acp:b", "fresh", now.Add(-time.Hour))
	h := &hosted{key: "acp:c", agent: "codex", sessionID: "running", cwd: "/w"}
	h.register()
	t.Cleanup(func() { h.retire() })
	mk(h.key, "running", now.Add(-72*time.Hour))
	waitForTranscriptWrites()

	if n := pruneStoredSessions(24*time.Hour, now); n != 1 {
		t.Errorf("pruned %d, want 1 (only the old finished one)", n)
	}
	if _, err := os.Stat(transcriptPath("old")); !os.IsNotExist(err) {
		t.Error("old session survived the prune")
	}
	if _, err := os.Stat(transcriptPath("fresh")); err != nil {
		t.Error("fresh session was pruned")
	}
	if _, err := os.Stat(transcriptPath("running")); err != nil {
		t.Error("a RUNNING session was pruned")
	}

	if n := pruneStoredSessions(0, now); n != 1 {
		t.Errorf("prune-all deleted %d, want 1 (the fresh one; the running one stays)", n)
	}
	if _, err := os.Stat(transcriptPath("running")); err != nil {
		t.Error("prune-all took the running session")
	}
}
