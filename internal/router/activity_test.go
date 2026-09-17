package router

import (
	"context"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/activity"
	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// journalRouter is a router with a journal in a temp dir and a connected
// shell whose catalog has been drained.
func journalRouter(t *testing.T, cfg Config) (*Router, *logCapture, wire.FrameTransport, func()) {
	t.Helper()
	lc := &logCapture{}
	if cfg.ActivityDir == "" && !cfg.NoActivity {
		cfg.ActivityDir = t.TempDir()
	}
	r := NewRouter(cfg, NewRegistry(), lc.log)
	pair := wiretest.NewPipePair()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = r.HandleShell(context.Background(), pair.EndA())
	}()
	if _, ok := readCtrl(t, pair.EndB()).(wire.ShellCatalog); !ok {
		t.Fatalf("expected ShellCatalog first")
	}
	return r, lc, pair.EndB(), func() {
		pair.Close()
		waitClose(t, done)
		r.CloseJournal()
	}
}

// readCtrlOfType reads control frames until one of type T arrives; the
// shell channel also carries session patches and telemetry.
func readCtrlOfType[T any](t *testing.T, e wire.FrameTransport) T {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if m, ok := readCtrl(t, e).(T); ok {
			return m
		}
	}
	var zero T
	t.Fatalf("no %T within 3s", zero)
	return zero
}

// A browser connecting is the first fact any journal holds, and the
// shell's own query verb is how a Timeline reads it back.
func TestJournalRecordsShellAttachAndAnswersQuery(t *testing.T) {
	r, _, shell, cleanup := journalRouter(t, Config{})
	defer cleanup()

	writeCtrl(t, shell, wire.NewShellActivityQuery(7))
	ok := readCtrlOfType[wire.ShellActivityQueryOK](t, shell)
	if ok.ReqID != 7 || len(ok.Entries) != 1 || ok.Entries[0].Kind != "session.attach" {
		t.Fatalf("query answered %+v", ok)
	}
	if ok.Entries[0].Host != "local" || ok.Entries[0].Seq == 0 {
		t.Fatalf("entry identity: %+v", ok.Entries[0])
	}
	_ = r
}

// With the journal off, the shell learns so rather than seeing an empty
// timeline it might mistake for a quiet day.
func TestJournalOffAnswersNotFound(t *testing.T) {
	_, _, shell, cleanup := journalRouter(t, Config{NoActivity: true})
	defer cleanup()
	writeCtrl(t, shell, wire.NewShellActivityQuery(1))
	e := readCtrlOfType[wire.ShellActivityQueryErr](t, shell)
	if e.ReqID != 1 || e.Code != wire.ErrCodeNotFound {
		t.Fatalf("err=%+v", e)
	}
}

// A tail pushes each new entry to the shell as it lands, and stops when
// asked.
func TestJournalTailPushesNewEntries(t *testing.T) {
	r, _, shell, cleanup := journalRouter(t, Config{})
	defer cleanup()

	writeCtrl(t, shell, wire.NewShellActivityTail(true))
	// The tail is armed asynchronously on the read loop; give it a beat.
	time.Sleep(20 * time.Millisecond)
	r.note(activity.Entry{Kind: "peer.up", Title: "b", Line: "remote host b connected"})
	got := readCtrlOfType[wire.ShellActivityEntry](t, shell)
	if got.Entry.Kind != "peer.up" || got.Entry.Title != "b" {
		t.Fatalf("tail delivered %+v", got.Entry)
	}

	writeCtrl(t, shell, wire.NewShellActivityTail(false))
	time.Sleep(20 * time.Millisecond)
	r.note(activity.Entry{Kind: "peer.down", Line: "gone"})
	// Nothing else should arrive: ask for stats and expect it to be the
	// next thing on the wire, not a stray entry.
	writeCtrl(t, shell, wire.NewShellActivityStats(3))
	m := readCtrl(t, shell)
	if _, stray := m.(wire.ShellActivityEntry); stray {
		t.Fatal("tail kept pushing after it was turned off")
	}
}

// Stats say what is stored; clear removes it and says so.
func TestJournalStatsAndClear(t *testing.T) {
	r, lc, shell, cleanup := journalRouter(t, Config{})
	defer cleanup()
	r.note(activity.Entry{Kind: "window.open", Line: "Terminal"})

	writeCtrl(t, shell, wire.NewShellActivityStats(5))
	st := readCtrlOfType[wire.ShellActivityStatsOK](t, shell)
	if st.ReqID != 5 || !st.Stats.Enabled || st.Stats.Today["window.open"] != 1 || st.Stats.Today["session.attach"] != 1 {
		t.Fatalf("stats=%+v", st.Stats)
	}

	writeCtrl(t, shell, wire.NewShellActivityClear(6))
	if ok := readCtrlOfType[wire.ShellActivityClearOK](t, shell); ok.ReqID != 6 {
		t.Fatalf("clear ok=%+v", ok)
	}
	writeCtrl(t, shell, wire.NewShellActivityQuery(8))
	if q := readCtrlOfType[wire.ShellActivityQueryOK](t, shell); len(q.Entries) != 0 {
		t.Fatalf("%d entries after clear", len(q.Entries))
	}
	if !lc.contains("activity: cleared by conn=") {
		t.Fatal("clear left no audit line")
	}
}

// noteInstance is an app instance with the given manifest, wired to r.
func noteInstance(r *Router, caps ...string) *AppInstance {
	m := aboutManifest()
	m.Capabilities = caps
	return &AppInstance{AppID: m.ID, InstanceID: "i-9", WindowID: 4, Manifest: m, router: r}
}

// The router stamps who spoke: a note carries the attested app and
// instance, never anything the payload claims.
func TestActivityNoteIsAttestedAndGated(t *testing.T) {
	r, lc, _, cleanup := journalRouter(t, Config{})
	defer cleanup()

	// Without the capability: refused, logged, nothing journaled.
	bare := noteInstance(r)
	_ = bare.handleActivityNote(wire.NewEvtActivityNote("agent.turn", "turn 1"))
	if !lc.contains("lacks CapActivityNote") {
		t.Fatal("a note without the capability was not refused")
	}

	inst := noteInstance(r, CapActivityNote)
	n := wire.NewEvtActivityNote("agent.turn", "turn 12 done")
	n.Intent = &wire.ActivityIntent{Kind: "resume", SessionID: "s1"}
	_ = inst.handleActivityNote(n)
	// A window the instance does not own is refused.
	other := wire.NewEvtActivityNote("x", "y")
	other.Win = 99
	_ = inst.handleActivityNote(other)
	if !lc.contains("win=99 not owned") {
		t.Fatal("a note naming another window was not refused")
	}

	res := r.journal.Query(activity.Query{Kinds: []string{"agent."}})
	if len(res.Entries) != 1 {
		t.Fatalf("journaled %d agent entries, want 1", len(res.Entries))
	}
	e := res.Entries[0]
	if e.App != "com.wash.about" || e.Instance != "i-9" || e.Line != "turn 12 done" {
		t.Fatalf("entry not stamped from the sender: %+v", e)
	}
	if e.Intent == nil || e.Intent.AppID != "com.wash.about" || e.Intent.SessionID != "s1" {
		t.Fatalf("intent not completed: %+v", e.Intent)
	}
}

// A chatty instance is bounded: past the burst its notes drop, and the
// journal says so once per drop in the log rather than filling up.
func TestActivityNoteIsRateLimited(t *testing.T) {
	r, lc, _, cleanup := journalRouter(t, Config{})
	defer cleanup()
	inst := noteInstance(r, CapActivityNote)
	for i := 0; i < noteBurst*2; i++ {
		_ = inst.handleActivityNote(wire.NewEvtActivityNote("agent.chunk", "…"))
	}
	res := r.journal.Query(activity.Query{Kinds: []string{"agent.chunk"}, Limit: 1000})
	if len(res.Entries) > noteBurst+1 {
		t.Fatalf("%d notes got through a burst of %d", len(res.Entries), noteBurst)
	}
	if !lc.contains("kind=agent.chunk: rate") {
		t.Fatal("dropped notes left no trace")
	}
}

// The limiter refills over time, so a normal cadence is never throttled.
func TestNoteLimiterRefills(t *testing.T) {
	var l noteLimiter
	now := time.Unix(1_700_000_000, 0)
	for i := 0; i < noteBurst; i++ {
		if !l.allow(now) {
			t.Fatalf("burst refused at %d", i)
		}
	}
	if l.allow(now) {
		t.Fatal("burst not bounded")
	}
	if !l.allow(now.Add(time.Second)) {
		t.Fatal("did not refill after a second")
	}
}
