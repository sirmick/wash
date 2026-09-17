// Package activity is the router's journal of what happened on this host
// (docs/COMMANDER.md §3): one bounded line and a pointer per event, on disk,
// queryable by time. It answers "what happened today, the last hour, while I
// was away" — the questions a summarizer that re-reads windows never can,
// because the interesting work happens when nobody is watching.
//
// The store is deterministic and knows nothing about models. It stores
// pointers, never bodies: no transcript text, no scrollback, no file
// contents. Every entry may carry an Intent — the way back to the thing it
// describes — so a timeline row is a jump and a rollup can end in actions.
//
// Writes never touch the caller's goroutine beyond an enqueue: the frame
// path that produces most entries must not wait on a disk. A full queue
// drops the entry and counts it, the same bargain QoS makes on the socket.
package activity

import (
	"bufio"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// MaxLine bounds Entry.Line. Long enough for "turn 12 done: edited
// web/shell/src/ws.ts, ran make unit-test (ok)"; short enough that a
// chatty source cannot turn the journal into a transcript.
const MaxLine = 200

// Intent is the way back to what an entry describes — exactly the shapes
// the start menu's Recent pop-outs and the agent verbs already accept, so
// a consumer needs no new plumbing to act on a row.
type Intent struct {
	// Kind is focus | resume | open.
	Kind string `json:"kind"`
	// Origin names the host for focus; empty means the entry's own.
	Origin     string `json:"origin,omitempty"`
	AppID      string `json:"app_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	WindowID   uint32 `json:"window_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RowKey     string `json:"row_key,omitempty"`
	Path       string `json:"path,omitempty"`
}

// Entry is one journal row. (Host, Seq) is its identity.
type Entry struct {
	TS   int64  `json:"ts"`
	Seq  uint64 `json:"seq"`
	Host string `json:"host"`
	// Kind is dotted and namespaced by source: window.open, open.routed,
	// session.attach, agent.turn, priv.escalate, brief, rollup …
	Kind     string `json:"kind"`
	App      string `json:"app,omitempty"`
	Instance string `json:"instance,omitempty"`
	Window   uint32 `json:"window,omitempty"`
	Title    string `json:"title,omitempty"`
	// Line is plain text, at most MaxLine bytes; Truncated says the store
	// cut it. Ref is source-defined (a transcript seq, a path) and small.
	Line      string         `json:"line"`
	Truncated bool           `json:"truncated,omitempty"`
	Ref       map[string]any `json:"ref,omitempty"`
	Intent    *Intent        `json:"intent,omitempty"`
}

// Options configure a Store. Zero values take the defaults below.
type Options struct {
	// Host is stamped on every entry ("local" for the seat's own router).
	Host string
	// Retention is how long a day file is kept. Default 30 days.
	Retention time.Duration
	// MaxBytes caps the directory; oldest days go first. Default 64 MiB.
	MaxBytes int64
	// QueueSize bounds entries waiting for the writer. Default 1024.
	QueueSize int
	// Now is the clock (tests).
	Now func() time.Time
}

const (
	defaultRetention = 30 * 24 * time.Hour
	defaultMaxBytes  = 64 << 20
	defaultQueue     = 1024
	flushEvery       = 250 * time.Millisecond
)

// Store is the journal for one host. A nil *Store is a disabled journal:
// every method is safe to call and does nothing, which is what
// --no-activity means.
type Store struct {
	dir  string
	opts Options

	mu      sync.Mutex
	seq     uint64
	dropped uint64
	subs    map[uint64]chan Entry
	nextSub uint64

	queue chan Entry
	done  chan struct{}
	wg    sync.WaitGroup

	// writer state, owned by the writer goroutine
	day  string
	file *os.File
	w    *bufio.Writer
}

// Open creates the directory if needed, resumes the sequence from the
// newest file, applies retention, and starts the writer.
func Open(dir string, opts Options) (*Store, error) {
	if dir == "" {
		return nil, errors.New("activity: empty dir")
	}
	if opts.Retention <= 0 {
		opts.Retention = defaultRetention
	}
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = defaultMaxBytes
	}
	if opts.QueueSize <= 0 {
		opts.QueueSize = defaultQueue
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Host == "" {
		opts.Host = "local"
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, opts: opts, subs: map[uint64]chan Entry{}, queue: make(chan Entry, opts.QueueSize), done: make(chan struct{})}
	s.seq = s.lastSeq()
	s.prune()
	s.wg.Add(1)
	go s.writer()
	return s, nil
}

// Close flushes and stops the writer. Idempotent.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
		close(s.done)
	}
	s.mu.Unlock()
	s.wg.Wait()
}

// Enabled reports whether entries go anywhere.
func (s *Store) Enabled() bool { return s != nil }

// Append records e. TS, Seq and Host are assigned when zero/empty; Line
// is bounded. The call never blocks: a full queue drops the entry and
// counts it (Stats.Dropped).
func (s *Store) Append(e Entry) {
	if s == nil {
		return
	}
	if e.TS == 0 {
		e.TS = s.opts.Now().UnixMilli()
	}
	if e.Host == "" {
		e.Host = s.opts.Host
	}
	e.Line, e.Truncated = bound(e.Line)
	if e.Kind == "" {
		e.Kind = "note"
	}
	s.mu.Lock()
	select {
	case <-s.done:
		s.mu.Unlock()
		return
	default:
	}
	s.seq++
	e.Seq = s.seq
	select {
	case s.queue <- e:
	default:
		s.dropped++
		s.mu.Unlock()
		return
	}
	for _, ch := range s.subs {
		select {
		case ch <- e:
		default: // a slow tail loses entries rather than holding the journal
		}
	}
	s.mu.Unlock()
}

// bound cuts a line to MaxLine bytes at a rune boundary, collapsing
// newlines: a line is one line.
func bound(line string) (string, bool) {
	line = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(line, "\r", " "), "\n", " "))
	if len(line) <= MaxLine {
		return line, false
	}
	end := MaxLine
	for end > 0 && !utf8.RuneStart(line[end]) {
		end--
	}
	return strings.TrimSpace(line[:end]) + "…", true
}

// Tail subscribes to new entries. The channel holds buf entries; a
// subscriber that falls behind misses entries rather than blocking the
// writer. cancel unsubscribes and closes the channel.
func (s *Store) Tail(buf int) (<-chan Entry, func()) {
	if s == nil {
		ch := make(chan Entry)
		close(ch)
		return ch, func() {}
	}
	if buf <= 0 {
		buf = 64
	}
	ch := make(chan Entry, buf)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			s.mu.Lock()
			delete(s.subs, id)
			s.mu.Unlock()
			close(ch)
		})
	}
}

// Query selects entries newest-first.
type Query struct {
	// From/To bound TS (unix ms), inclusive; zero means unbounded.
	From, To int64
	// Kinds and Apps filter exactly; a Kind ending in "." matches the
	// prefix (window. matches window.open).
	Kinds, Apps []string
	// Text is a case-insensitive substring over Title and Line.
	Text string
	// Limit caps the page (default 100, max 1000). Cursor continues a
	// page from a previous Result.
	Limit  int
	Cursor string
}

// Result is one page.
type Result struct {
	Entries []Entry `json:"entries"`
	// Cursor is set when more entries precede the page.
	Cursor string `json:"cursor,omitempty"`
}

// Query answers q from disk plus what the writer has not flushed yet. The
// files are the truth; the queue is drained by Flush first so a query
// straight after an Append sees it.
func (s *Store) Query(q Query) Result {
	if s == nil {
		return Result{Entries: []Entry{}}
	}
	s.Flush()
	if q.Limit <= 0 {
		q.Limit = 100
	}
	if q.Limit > 1000 {
		q.Limit = 1000
	}
	var beforeTS int64
	var beforeSeq uint64
	if q.Cursor != "" {
		if ts, seq, ok := parseCursor(q.Cursor); ok {
			beforeTS, beforeSeq = ts, seq
		}
	}
	text := strings.ToLower(strings.TrimSpace(q.Text))
	days := s.days() // oldest first
	out := make([]Entry, 0, q.Limit+1)
	for i := len(days) - 1; i >= 0 && len(out) <= q.Limit; i-- {
		d := days[i]
		if q.From != 0 && dayEnd(d) < q.From {
			break
		}
		if q.To != 0 && dayStart(d) > q.To {
			continue
		}
		entries := s.readDay(d)
		for j := len(entries) - 1; j >= 0; j-- {
			e := entries[j]
			if beforeTS != 0 && (e.TS > beforeTS || (e.TS == beforeTS && e.Seq >= beforeSeq)) {
				continue
			}
			if q.From != 0 && e.TS < q.From {
				continue
			}
			if q.To != 0 && e.TS > q.To {
				continue
			}
			if !matchKind(e.Kind, q.Kinds) || !matchExact(e.App, q.Apps) {
				continue
			}
			if text != "" && !strings.Contains(strings.ToLower(e.Title), text) && !strings.Contains(strings.ToLower(e.Line), text) {
				continue
			}
			out = append(out, e)
			if len(out) > q.Limit {
				break
			}
		}
	}
	res := Result{Entries: out}
	if len(out) > q.Limit {
		res.Entries = out[:q.Limit]
		last := res.Entries[q.Limit-1]
		res.Cursor = strconv.FormatInt(last.TS, 10) + ":" + strconv.FormatUint(last.Seq, 10)
	}
	if res.Entries == nil {
		res.Entries = []Entry{}
	}
	return res
}

func parseCursor(c string) (int64, uint64, bool) {
	i := strings.IndexByte(c, ':')
	if i < 0 {
		return 0, 0, false
	}
	ts, err1 := strconv.ParseInt(c[:i], 10, 64)
	seq, err2 := strconv.ParseUint(c[i+1:], 10, 64)
	return ts, seq, err1 == nil && err2 == nil
}

func matchKind(kind string, kinds []string) bool {
	if len(kinds) == 0 {
		return true
	}
	for _, k := range kinds {
		if k == kind || (strings.HasSuffix(k, ".") && strings.HasPrefix(kind, k)) {
			return true
		}
	}
	return false
}

func matchExact(v string, set []string) bool {
	if len(set) == 0 {
		return true
	}
	for _, x := range set {
		if x == v {
			return true
		}
	}
	return false
}

// Stats is what About shows: what is stored, and what was dropped.
type Stats struct {
	Days    int              `json:"days"`
	Bytes   int64            `json:"bytes"`
	Dropped uint64           `json:"dropped"`
	Today   map[string]int   `json:"today"` // entries by kind, today
	Seq     uint64           `json:"seq"`
	Path    string           `json:"path"`
	Enabled bool             `json:"enabled"`
	Kinds   map[string]int64 `json:"-"`
}

// Stats reports the store's footprint.
func (s *Store) Stats() Stats {
	if s == nil {
		return Stats{Today: map[string]int{}}
	}
	s.Flush()
	st := Stats{Today: map[string]int{}, Path: s.dir, Enabled: true}
	days := s.days()
	st.Days = len(days)
	for _, d := range days {
		if fi, err := os.Stat(s.dayPath(d)); err == nil {
			st.Bytes += fi.Size()
		}
	}
	for _, e := range s.readDay(s.dayOf(s.opts.Now())) {
		st.Today[e.Kind]++
	}
	s.mu.Lock()
	st.Dropped, st.Seq = s.dropped, s.seq
	s.mu.Unlock()
	return st
}

// Clear deletes every day file. The sequence keeps counting, so an old
// cursor can never alias a new entry.
func (s *Store) Clear() error {
	if s == nil {
		return nil
	}
	// The writer must let go of today's file first: removing a file it
	// still holds open would leave the next entry in an unlinked inode
	// that no query can see.
	s.control(resetKind)
	var first error
	for _, d := range s.days() {
		if err := os.Remove(s.dayPath(d)); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// control sends the writer a command and waits for it to be done.
func (s *Store) control(kind string) {
	select {
	case <-s.done:
		return
	default:
	}
	ack := make(chan struct{})
	select {
	case s.queue <- Entry{Kind: kind, Ref: map[string]any{"ack": ack}}:
		<-ack
	case <-s.done:
	}
}

// Flush waits for the queue to drain and the file to be written. Queries
// and tests use it; the frame path never does.
func (s *Store) Flush() {
	if s == nil {
		return
	}
	select {
	case <-s.done:
		return
	default:
	}
	s.control(flushKind)
}

const (
	flushKind = "\x00flush"
	resetKind = "\x00reset"
)

// writer is the one goroutine that touches the files.
func (s *Store) writer() {
	defer s.wg.Done()
	t := time.NewTicker(flushEvery)
	defer t.Stop()
	for {
		select {
		case e := <-s.queue:
			if s.handleControl(e) {
				continue
			}
			s.write(e)
		case <-t.C:
			s.flushFile()
		case <-s.done:
			// Drain what is queued, then stop.
			for {
				select {
				case e := <-s.queue:
					if s.handleControl(e) {
						continue
					}
					s.write(e)
				default:
					s.flushFile()
					s.closeFile()
					return
				}
			}
		}
	}
}

// handleControl runs a writer command and acks it; false for a real entry.
func (s *Store) handleControl(e Entry) bool {
	switch e.Kind {
	case flushKind:
		s.flushFile()
	case resetKind:
		s.flushFile()
		s.closeFile()
	default:
		return false
	}
	if ack, ok := e.Ref["ack"].(chan struct{}); ok {
		close(ack)
	}
	return true
}

func (s *Store) write(e Entry) {
	day := s.dayOf(time.UnixMilli(e.TS))
	if day != s.day || s.file == nil {
		s.flushFile()
		s.closeFile()
		f, err := os.OpenFile(s.dayPath(day), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			s.mu.Lock()
			s.dropped++
			s.mu.Unlock()
			return
		}
		s.file, s.w, s.day = f, bufio.NewWriter(f), day
		s.prune()
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.w.Write(b)
	s.w.WriteByte('\n')
}

func (s *Store) flushFile() {
	if s.w != nil {
		_ = s.w.Flush()
	}
}

func (s *Store) closeFile() {
	if s.file != nil {
		_ = s.file.Close()
		s.file, s.w, s.day = nil, nil, ""
	}
}

// --- days on disk ---

const dayLayout = "2006-01-02"

func (s *Store) dayOf(t time.Time) string { return t.Local().Format(dayLayout) }
func (s *Store) dayPath(day string) string {
	return filepath.Join(s.dir, day+".jsonl")
}

func dayStart(day string) int64 {
	t, err := time.ParseInLocation(dayLayout, day, time.Local)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}

func dayEnd(day string) int64 {
	t, err := time.ParseInLocation(dayLayout, day, time.Local)
	if err != nil {
		return 0
	}
	return t.Add(24*time.Hour).UnixMilli() - 1
}

// days lists the day files, oldest first.
func (s *Store) days() []string {
	ents, err := os.ReadDir(s.dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		day := strings.TrimSuffix(name, ".jsonl")
		if _, err := time.ParseInLocation(dayLayout, day, time.Local); err != nil {
			continue
		}
		out = append(out, day)
	}
	sort.Strings(out)
	return out
}

// readDay parses one file. A torn last line (a crash mid-write) is
// skipped, not fatal.
func (s *Store) readDay(day string) []Entry {
	f, err := os.Open(s.dayPath(day))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Kind != "" {
			out = append(out, e)
		}
	}
	return out
}

// lastSeq resumes the sequence from the newest file so (host, seq) stays
// unique across restarts.
func (s *Store) lastSeq() uint64 {
	days := s.days()
	if len(days) == 0 {
		return 0
	}
	var max uint64
	for _, e := range s.readDay(days[len(days)-1]) {
		if e.Seq > max {
			max = e.Seq
		}
	}
	return max
}

// prune applies retention by age, then by size, oldest day first. Never
// the current day.
func (s *Store) prune() {
	days := s.days()
	today := s.dayOf(s.opts.Now())
	cutoff := s.dayOf(s.opts.Now().Add(-s.opts.Retention))
	var total int64
	sizes := map[string]int64{}
	for _, d := range days {
		if fi, err := os.Stat(s.dayPath(d)); err == nil {
			sizes[d] = fi.Size()
			total += fi.Size()
		}
	}
	for _, d := range days {
		if d == today {
			continue
		}
		if d < cutoff || total > s.opts.MaxBytes {
			if err := os.Remove(s.dayPath(d)); err == nil {
				total -= sizes[d]
			}
		}
	}
}
