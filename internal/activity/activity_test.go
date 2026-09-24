package activity

import (
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// clock is a settable time the writer goroutine can read safely.
type clock struct{ ms atomic.Int64 }

func (c *clock) set(t time.Time) { c.ms.Store(t.UnixMilli()) }
func (c *clock) now() time.Time  { return time.UnixMilli(c.ms.Load()) }
func at(d int) time.Time         { return time.Date(2026, 9, d, 12, 0, 0, 0, time.Local) }

func open(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, dir
}

func TestAppendThenQueryNewestFirst(t *testing.T) {
	s, _ := open(t, Options{})
	s.Append(Entry{Kind: "window.open", App: "com.wash.term", Line: "Terminal"})
	s.Append(Entry{Kind: "window.focus", App: "com.wash.term", Line: "Terminal"})
	s.Append(Entry{Kind: "open.routed", App: "com.wash.edit", Line: "notes.md", Intent: &Intent{Kind: "open", Path: "/home/u/notes.md"}})

	r := s.Query(Query{})
	if len(r.Entries) != 3 {
		t.Fatalf("got %d entries", len(r.Entries))
	}
	if r.Entries[0].Kind != "open.routed" || r.Entries[2].Kind != "window.open" {
		t.Fatalf("not newest first: %v %v", r.Entries[0].Kind, r.Entries[2].Kind)
	}
	if r.Entries[0].Intent == nil || r.Entries[0].Intent.Path != "/home/u/notes.md" {
		t.Fatalf("intent lost: %+v", r.Entries[0].Intent)
	}
	if r.Entries[0].Seq != 3 || r.Entries[0].Host != "local" {
		t.Fatalf("identity: seq=%d host=%q", r.Entries[0].Seq, r.Entries[0].Host)
	}
}

func TestLineIsBoundedAtARuneBoundary(t *testing.T) {
	s, _ := open(t, Options{})
	long := strings.Repeat("é", MaxLine) // 2 bytes each: the cut lands mid-rune
	s.Append(Entry{Kind: "k", Line: long + "\nsecond line"})
	e := s.Query(Query{}).Entries[0]
	if !e.Truncated {
		t.Fatal("not marked truncated")
	}
	if len(e.Line) > MaxLine+len("…") || strings.Contains(e.Line, "\n") {
		t.Fatalf("line=%q (%d bytes)", e.Line, len(e.Line))
	}
	for _, r := range e.Line {
		if r == '�' {
			t.Fatal("cut mid-rune")
		}
	}
}

func TestQueryFiltersAndCursor(t *testing.T) {
	s, _ := open(t, Options{})
	for i := 0; i < 25; i++ {
		kind, app := "window.focus", "com.wash.term"
		if i%5 == 0 {
			kind, app = "agent.turn", "com.wash.agentd"
		}
		s.Append(Entry{Kind: kind, App: app, Title: "T", Line: "line " + string(rune('a'+i))})
	}
	// Prefix kinds, exact apps, text.
	if n := len(s.Query(Query{Kinds: []string{"window."}}).Entries); n != 20 {
		t.Fatalf("window.* = %d", n)
	}
	if n := len(s.Query(Query{Apps: []string{"com.wash.agentd"}}).Entries); n != 5 {
		t.Fatalf("agentd = %d", n)
	}
	if n := len(s.Query(Query{Text: "LINE B"}).Entries); n != 1 {
		t.Fatalf("text = %d", n)
	}
	// Pages of 10 walk all 25 without overlap or gap.
	seen := map[uint64]bool{}
	cur := ""
	for pages := 0; pages < 5; pages++ {
		r := s.Query(Query{Limit: 10, Cursor: cur})
		for _, e := range r.Entries {
			if seen[e.Seq] {
				t.Fatalf("seq %d twice", e.Seq)
			}
			seen[e.Seq] = true
		}
		if r.Cursor == "" {
			break
		}
		cur = r.Cursor
	}
	if len(seen) != 25 {
		t.Fatalf("paged %d of 25", len(seen))
	}
}

func TestDayFilesRotateAndRetentionPrunes(t *testing.T) {
	var c clock
	c.set(at(16))
	s, dir := open(t, Options{Now: c.now, Retention: 3 * 24 * time.Hour})
	for d := 10; d >= 0; d-- {
		c.set(at(16 - d))
		s.Append(Entry{Kind: "k", Line: "day"})
		s.Flush() // one rotation per day, in order
	}
	s.Flush()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	// Retention pruned on each rotation: today plus three days back survive.
	if len(files) > 4 {
		t.Fatalf("%d day files kept, want ≤4: %v", len(files), files)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "2026-09-06.jsonl") {
			t.Fatal("a day past retention survived")
		}
	}
	// Queries respect the range and never touch pruned days.
	r := s.Query(Query{From: time.Date(2026, 9, 15, 0, 0, 0, 0, time.Local).UnixMilli()})
	if len(r.Entries) != 2 {
		t.Fatalf("range query = %d, want 2", len(r.Entries))
	}
}

func TestSizeCapPrunesOldestFirst(t *testing.T) {
	var c clock
	c.set(at(16))
	s, dir := open(t, Options{Now: c.now, MaxBytes: 2000})
	for d := 3; d >= 0; d-- {
		c.set(at(16 - d))
		for i := 0; i < 8; i++ {
			s.Append(Entry{Kind: "k", Line: strings.Repeat("x", 100)})
		}
		s.Flush()
	}
	s.Flush()
	if _, err := os.Stat(filepath.Join(dir, "2026-09-13.jsonl")); !os.IsNotExist(err) {
		t.Fatal("oldest day survived the size cap")
	}
	if _, err := os.Stat(filepath.Join(dir, "2026-09-16.jsonl")); err != nil {
		t.Fatal("today was pruned")
	}
}

func TestSequenceResumesAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	s.Append(Entry{Kind: "k", Line: "one"})
	s.Append(Entry{Kind: "k", Line: "two"})
	s.Close()

	s2, err := Open(dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s2.Append(Entry{Kind: "k", Line: "three"})
	if got := s2.Query(Query{}).Entries[0].Seq; got != 3 {
		t.Fatalf("seq after reopen = %d, want 3", got)
	}
}

func TestFullQueueDropsAndCounts(t *testing.T) {
	dir := t.TempDir()
	// A store whose writer never runs: everything past the queue drops.
	s := &Store{dir: dir, opts: Options{Now: time.Now, Host: "local", QueueSize: 2}, subs: map[uint64]chan Entry{}, queue: make(chan Entry, 2), done: make(chan struct{})}
	for i := 0; i < 5; i++ {
		s.Append(Entry{Kind: "k", Line: "x"})
	}
	s.mu.Lock()
	dropped := s.dropped
	s.mu.Unlock()
	if dropped != 3 {
		t.Fatalf("dropped = %d, want 3", dropped)
	}
}

func TestTailDeliversNewEntries(t *testing.T) {
	s, _ := open(t, Options{})
	ch, cancel := s.Tail(4)
	defer cancel()
	s.Append(Entry{Kind: "window.open", Line: "hi"})
	select {
	case e := <-ch:
		if e.Kind != "window.open" || e.Seq != 1 {
			t.Fatalf("tail got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("tail delivered nothing")
	}
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("cancel did not close the tail")
	}
}

func TestClearAndStats(t *testing.T) {
	s, _ := open(t, Options{})
	s.Append(Entry{Kind: "window.open", Line: "a"})
	s.Append(Entry{Kind: "window.open", Line: "b"})
	st := s.Stats()
	if st.Days != 1 || st.Bytes == 0 || st.Today["window.open"] != 2 || !st.Enabled {
		t.Fatalf("stats = %+v", st)
	}
	if err := s.Clear(); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Query(Query{}).Entries); n != 0 {
		t.Fatalf("%d entries after clear", n)
	}
	// The sequence keeps counting so an old cursor can never alias.
	s.Append(Entry{Kind: "k", Line: "c"})
	if got := s.Query(Query{}).Entries[0].Seq; got != 3 {
		t.Fatalf("seq after clear = %d", got)
	}
}

func TestATornLastLineIsSkipped(t *testing.T) {
	s, dir := open(t, Options{})
	s.Append(Entry{Kind: "k", Line: "whole"})
	s.Flush()
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	f, _ := os.OpenFile(files[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"ts":1,"seq":2,"kind":"k","line":"torn`)
	f.Close()
	r := s.Query(Query{})
	if len(r.Entries) != 1 || r.Entries[0].Line != "whole" {
		t.Fatalf("torn line handling: %+v", r.Entries)
	}
}

func TestNilStoreIsADisabledJournal(t *testing.T) {
	var s *Store
	s.Append(Entry{Kind: "k"})
	if r := s.Query(Query{}); len(r.Entries) != 0 {
		t.Fatal("nil store answered")
	}
	if s.Stats().Enabled {
		t.Fatal("nil store enabled")
	}
	ch, cancel := s.Tail(1)
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("nil tail open")
	}
	s.Close()
}
