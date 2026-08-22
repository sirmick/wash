package agentd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The index is a cache in front of a scan, so the property that matters
// is not "it is fast" but "it never hides a session the scan would have
// found". Every test here is about that, or about the index surviving
// the states a cache gets into.

func seedTwo(t *testing.T) time.Time {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "s-reconnect", "codex", "/home/mick/wash", now)
	writeSummary("s-reconnect", transcriptSummary{Agent: "codex", Cwd: "/home/mick/wash"})
	appendPrompt("acp:1", "why does the banner race on reconnect", now)

	bindTranscript("acp:2", "s-radio", "claude", "/home/mick/radio", now.Add(time.Minute))
	writeSummary("s-radio", transcriptSummary{Agent: "claude", Cwd: "/home/mick/radio"})
	appendPrompt("acp:2", "add somafm stations to the list", now.Add(time.Minute))
	waitForTranscriptWrites()
	return now
}

func TestIndexNarrowsWithoutLosingHits(t *testing.T) {
	withStateDir(t)
	seedTwo(t)

	cand, ok := searchCandidates([]string{"reconnect"})
	if !ok {
		t.Fatal("index declined a three-letter-plus term")
	}
	if !cand["s-reconnect"] {
		t.Error("the session containing the word is not a candidate")
	}
	if cand["s-radio"] {
		t.Error("an unrelated session was offered as a candidate")
	}
}

func TestIndexAnswersMidWordSoTypingWorks(t *testing.T) {
	// The panel searches as you type. "recon" has to find "reconnect"
	// before the word is finished — the reason this is a trigram index
	// and not a word index.
	withStateDir(t)
	seedTwo(t)

	for _, q := range []string{"rec", "recon", "reconnec", "reconnect"} {
		cand, ok := searchCandidates([]string{q})
		if !ok || !cand["s-reconnect"] {
			t.Errorf("query %q did not reach the session (ok=%v)", q, ok)
		}
	}
}

func TestIndexDeclinesTooShortRatherThanLying(t *testing.T) {
	// Under three characters there is no trigram. Saying "no candidates"
	// would silently hide every match; saying "I can't narrow this" makes
	// the caller scan, which is correct and merely slower.
	withStateDir(t)
	seedTwo(t)

	if _, ok := searchCandidates([]string{"on"}); ok {
		t.Error("index claimed to answer a two-character term")
	}
	// And the query still works end to end through the scan path: "on"
	// is in "reconnect" and in "stations", and nowhere in either
	// session's metadata, so only the fallback scan can find both.
	if got := historyQuery("on", 0); len(got) != 2 {
		t.Errorf("two-character query returned %v, want both sessions", ids(got))
	}
}

func TestIndexPicksUpAppendsToALiveSession(t *testing.T) {
	// The one file being appended to is the one a search most needs to
	// see. Reconcile is by (size, mtime), so this is the case that proves
	// the staleness check works at all.
	withStateDir(t)
	now := seedTwo(t)

	if got := historyQuery("wombat", 0); len(got) != 0 {
		t.Fatalf("wombat matched before it was written: %v", ids(got))
	}
	appendPrompt("acp:1", "now about the wombat protocol", now.Add(time.Hour))
	waitForTranscriptWrites()
	// mtime has 1s granularity on some filesystems; the size changed
	// either way, which is the check that matters here.
	got := historyQuery("wombat", 0)
	if len(got) != 1 || got[0].SessionID != "s-reconnect" {
		t.Errorf("after append, wombat = %v, want s-reconnect", ids(got))
	}
}

func TestIndexDropsDeletedSessions(t *testing.T) {
	withStateDir(t)
	seedTwo(t)
	if _, err := os.Stat(transcriptPath("s-radio")); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	if err := os.Remove(transcriptPath("s-radio")); err != nil {
		t.Fatal(err)
	}
	cand, ok := searchCandidates([]string{"somafm"})
	if !ok {
		t.Fatal("index declined")
	}
	if cand["s-radio"] {
		t.Error("a deleted session is still a candidate")
	}
	if n, _ := indexStats(); n != 1 {
		t.Errorf("index holds %d sessions, want 1", n)
	}
}

func TestIndexSurvivesAGarbageCacheFile(t *testing.T) {
	// The file is derived, so every unreadable shape is a rebuild rather
	// than an error. Anything else would turn a corrupt cache into lost
	// history.
	withStateDir(t)
	seedTwo(t)
	searchCandidates([]string{"reconnect"}) // write the cache

	for _, junk := range []string{"", "not json at all", `{"v":999,"e":[]}`} {
		if err := os.WriteFile(indexPath(), []byte(junk), 0o600); err != nil {
			t.Fatal(err)
		}
		resetIndexForTest()
		cand, ok := searchCandidates([]string{"reconnect"})
		if !ok || !cand["s-reconnect"] {
			t.Errorf("cache %q was not rebuilt (ok=%v cand=%v)", junk, ok, cand)
		}
	}
}

func TestIndexCacheIsWrittenAndReused(t *testing.T) {
	withStateDir(t)
	seedTwo(t)
	searchCandidates([]string{"reconnect"})

	b, err := os.ReadFile(indexPath())
	if err != nil {
		t.Fatalf("no cache written: %v", err)
	}
	if !strings.Contains(string(b), "s-reconnect") {
		t.Error("cache does not name the sessions it indexed")
	}
	// A fresh process (reset + load) answers from the file without
	// re-reading the transcripts.
	resetIndexForTest()
	if cand, ok := searchCandidates([]string{"reconnect"}); !ok || !cand["s-reconnect"] {
		t.Error("loaded cache did not answer")
	}
}

func TestIndexFileIsNotMistakenForATranscript(t *testing.T) {
	// It lives beside the .jsonl files; the history list walks that
	// directory. A cache that showed up as a session would be a bug in
	// the most visible place.
	withStateDir(t)
	seedTwo(t)
	searchCandidates([]string{"reconnect"})

	if _, err := os.Stat(indexPath()); err != nil {
		t.Fatalf("cache missing: %v", err)
	}
	if got := historyQuery("", 0); len(got) != 2 {
		t.Errorf("history lists %v, want exactly the two transcripts", ids(got))
	}
	if filepath.Ext(indexPath()) == ".jsonl" {
		t.Error("cache is named like a transcript")
	}
}

func TestIndexIgnoresImagePayloads(t *testing.T) {
	// An image event's Text is base64. Indexing it would make searches
	// match noise no human typed — and the scan skips it, so the index
	// agreeing is what keeps the two consistent.
	withStateDir(t)
	now := time.Unix(1_700_000_000, 0)
	bindTranscript("acp:1", "s-img", "codex", "/home/mick", now)
	appendEvent("acp:1", Event{Kind: EventImage, Mime: "image/png", Text: strings.Repeat("QUJD", 40)}, now)
	waitForTranscriptWrites()

	if cand, ok := searchCandidates([]string{"QUJD"}); ok && cand["s-img"] {
		t.Error("base64 image payload is in the index")
	}
}
