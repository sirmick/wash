// Transcript persistence (GH #21).
//
// transcript.go already captures every session live — it just captures it
// into memory, for the router's lifetime, and nothing ever deletes from
// `trans`. So a conversation dies with the router, and a long-lived
// router accumulates every transcript it has ever seen. This gives that
// capture a disk tail, which is both the history feature and the fix for
// the leak.
//
// Format: one JSON object per line, at
//
//	$XDG_STATE_HOME/wash/agent-transcripts/<session-id>.jsonl
//
// The first line is a meta record naming the session; every later line is
// an Event. An UPDATE (a tool row moving pending → in_progress →
// completed) appends a NEW line carrying the SAME seq, and loading folds
// by seq with last-write-wins.
//
// Append-only is what makes writing to a live session safe: a crash or a
// full disk truncates at a line boundary and costs at most the last
// event, where rewriting a file in place can cost the whole thing. It is
// also why the writer never seeks — the cost is that a session which
// updates one tool call many times writes several lines for it, which
// fold away on load and are worth the durability.
//
// Ordering, and why the writer is its own goroutine: push() runs under
// transMu on the ACP read path, which must not block (appendUpdate's
// contract). Writing there would put a disk round-trip inside the lock
// every session shares. So push enqueues and a single writer drains,
// which also gives every session one writer rather than one per session.
package agentd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/sirmick/wash/internal/acp"
)

// storeQueue is deep enough that overflow means the disk has genuinely
// stalled rather than that a session is chatty: a streaming agent
// produces a few hundred events a minute, so this is minutes of slack.
const storeQueue = 8192

// transcriptMeta is the first line of a transcript file. Enough to
// reconstruct a Recent row from the file alone, so history survives even
// if agent-sessions.json is lost.
type transcriptMeta struct {
	Kind      string `json:"kind"`
	Version   int    `json:"v"`
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	StartedMS int64  `json:"started_ms"`
}

const (
	metaKind        = "meta"
	transcriptVer   = 1
	transcriptDirNM = "agent-transcripts"
)

// storeRec is one line to append, or — when line is nil — an order to
// close that session's open file. The close travels the SAME channel as
// the writes so it is ordered against them: rewriteTranscript renames a
// new file over the old one, and a writer still holding the old fd would
// append to an unlinked inode and silently lose every event after a
// resume. Ordering is the whole point; a side-channel close would race.
type storeRec struct {
	sessionID string
	line      []byte
}

var (
	storeMu sync.Mutex
	// storeSession maps a roster key to the session id its events belong
	// to. push() knows only the key; the file is named by session id,
	// because that is what survives a restart and what a resume asks for.
	storeSession = map[string]string{}
	storeCh      chan storeRec
	storeOnce    sync.Once
	// storeDropped counts events lost to a stalled disk. Logged once per
	// run rather than per event, which would itself be the stall.
	storeDropped int
	storeWarned  bool
	// storeIdle is pulsed by the writer whenever it has drained and
	// flushed, so rewrite and tests can wait on a fact rather than sleep.
	storeIdle = make(chan struct{}, 1)
)

// transcriptDir is $XDG_STATE_HOME/wash/agent-transcripts (else
// ~/.local/state/...), matching where agent-sessions.json already lives.
func transcriptDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "wash", transcriptDirNM)
}

// transcriptPath is where one session's transcript lives. Session ids come
// off adapter payloads, so the name is sanitised rather than trusted: a
// session id containing "/" or ".." must not choose the path.
func transcriptPath(sessionID string) string {
	dir := transcriptDir()
	if dir == "" || sessionID == "" {
		return ""
	}
	return filepath.Join(dir, safeFileName(sessionID)+".jsonl")
}

// safeFileName keeps the id readable while making it incapable of
// escaping the directory: everything outside [A-Za-z0-9._-] becomes "_",
// and a leading dot is escaped so no id can produce a hidden file or the
// names "." / "..".
func safeFileName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == '.' && b.Len() > 0:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	if len(s) > 200 {
		return b.String()[:200]
	}
	return b.String()
}

// bindTranscript ties a roster key to the session id its events persist
// under, and writes the meta line. Called once the adapter has answered
// with a session id — before that there is no name to file it under.
func bindTranscript(key, sessionID, agent, cwd string, now time.Time) {
	if key == "" || sessionID == "" {
		return
	}
	storeMu.Lock()
	storeSession[key] = sessionID
	storeMu.Unlock()

	// A resumed session appends to the file it already has; the meta line
	// is written only when the file is new, so reopening does not restate
	// it. Any error here disables persistence loudly rather than failing
	// every event quietly downstream.
	path := transcriptPath(sessionID)
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		log.Printf("agentd: transcript dir: %v", err)
		return
	}
	if _, err := os.Stat(path); err == nil {
		return // already has a meta line
	}
	line, err := json.Marshal(transcriptMeta{
		Kind: metaKind, Version: transcriptVer, SessionID: sessionID,
		Agent: agent, Cwd: cwd, StartedMS: now.UnixMilli(),
	})
	if err != nil {
		return
	}
	enqueue(sessionID, line)
}

// persistEvent queues one event for its session's file. Safe to call
// under transMu: it only appends to a channel.
func persistEvent(key string, e Event) {
	storeMu.Lock()
	sid := storeSession[key]
	storeMu.Unlock()
	if sid == "" {
		return
	}
	line, err := json.Marshal(e)
	if err != nil {
		return
	}
	enqueue(sid, line)
}

func enqueue(sessionID string, line []byte) {
	startStore()
	select {
	case storeCh <- storeRec{sessionID: sessionID, line: line}:
	default:
		// Never block the ACP read path. A full queue means the disk has
		// stopped keeping up, which is a fact about the machine — say it
		// once, keep the session running.
		storeMu.Lock()
		storeDropped++
		warn := !storeWarned
		storeWarned = true
		storeMu.Unlock()
		if warn {
			log.Printf("agentd: transcript queue full — events are being dropped; the disk is not keeping up")
		}
	}
}

// closeTranscriptFile asks the writer to release a session's file. Queued
// through the write channel so it cannot overtake pending appends.
func closeTranscriptFile(sessionID string) {
	if sessionID == "" {
		return
	}
	enqueue(sessionID, nil)
}

func startStore() {
	storeOnce.Do(func() {
		storeCh = make(chan storeRec, storeQueue)
		go runStore(storeCh)
	})
}

// runStore is the single writer. It holds one open file per session so a
// streaming turn is not a syscall storm of open/close, and flushes each
// batch before going idle so a reader (a test, or a resume) sees a
// complete file whenever the queue is empty.
func runStore(ch <-chan storeRec) {
	type sink struct {
		f *os.File
		w *bufio.Writer
	}
	// One open file per session, capped: a router that has seen hundreds
	// of sessions must not hold hundreds of descriptors. Over the cap the
	// oldest are closed and reopened on demand — appending, so reopening
	// costs a syscall and nothing else.
	const maxSinks = 32
	sinks := map[string]*sink{}
	defer func() {
		for _, s := range sinks {
			_ = s.w.Flush()
			_ = s.f.Close()
		}
	}()

	flushAll := func() {
		for id, s := range sinks {
			if err := s.w.Flush(); err != nil {
				log.Printf("agentd: transcript flush session=%s: %v", id, err)
			}
		}
	}

	closeSink := func(id string) {
		if s := sinks[id]; s != nil {
			if err := s.w.Flush(); err != nil {
				log.Printf("agentd: transcript flush session=%s: %v", id, err)
			}
			_ = s.f.Close()
			delete(sinks, id)
		}
	}

	write := func(rec storeRec) {
		if rec.line == nil {
			closeSink(rec.sessionID)
			return
		}
		s := sinks[rec.sessionID]
		if s == nil {
			path := transcriptPath(rec.sessionID)
			if path == "" {
				return
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				log.Printf("agentd: transcript dir: %v", err)
				return
			}
			// 0600: a transcript is everything the human typed and
			// everything the agent read back, which on a multi-user box
			// is nobody else's business.
			f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				log.Printf("agentd: transcript open session=%s: %v", rec.sessionID, err)
				return
			}
			if len(sinks) >= maxSinks {
				for id, old := range sinks {
					_ = old.w.Flush()
					_ = old.f.Close()
					delete(sinks, id)
					if len(sinks) < maxSinks {
						break
					}
				}
			}
			s = &sink{f: f, w: bufio.NewWriter(f)}
			sinks[rec.sessionID] = s
		}
		s.w.Write(rec.line)
		s.w.WriteByte('\n')
	}

	for rec := range ch {
		write(rec)
		// Drain whatever else is queued before flushing, so a streaming
		// turn costs one flush per batch rather than one per chunk.
		drained := true
		for drained {
			select {
			case next := <-ch:
				write(next)
			default:
				drained = false
			}
		}
		flushAll()
		// Signal the barrier non-blockingly: whoever is waiting wants to
		// know the queue reached empty, and a second signal adds nothing.
		select {
		case storeIdle <- struct{}{}:
		default:
		}
	}
}

// transcriptSessionID is the session a roster key's events belong to, or
// "" if that key was never bound. Kept after a session retires so
// snapshot() can still find the stored transcript.
func transcriptSessionID(key string) string {
	storeMu.Lock()
	defer storeMu.Unlock()
	return storeSession[key]
}

// loadTranscript reads a session's transcript back, folding updates by
// seq. Returns nil (no error) when the session has no file: a session
// from before persistence, or one that never produced an event, is an
// empty history rather than a failure.
func loadTranscript(sessionID string) ([]Event, error) {
	path := transcriptPath(sessionID)
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	// Order is the order events were FIRST seen; an update rewrites in
	// place, exactly as updateEvent does in memory, so a completed tool
	// call stays where it happened rather than jumping to the end.
	var out []Event
	at := map[uint64]int{}
	sc := bufio.NewScanner(f)
	// A single event can carry a base64 image up to maxImageBytes, and
	// bufio.Scanner's default 64KB line cap would silently truncate the
	// file at the first one.
	sc.Buffer(make([]byte, 0, 64*1024), maxImageBytes+(1<<16))
	line := 0
	for sc.Scan() {
		line++
		b := sc.Bytes()
		if len(b) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(b, &e); err != nil {
			// One malformed line is a truncated write, not a reason to
			// throw away the conversation around it.
			log.Printf("agentd: transcript %s line %d: %v", filepath.Base(path), line, err)
			continue
		}
		if e.Kind == metaKind || e.Seq == 0 {
			continue
		}
		if i, ok := at[e.Seq]; ok {
			out[i] = e
			continue
		}
		at[e.Seq] = len(out)
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		// Return what was read: a partial conversation beats none, and
		// the caller logs.
		return out, fmt.Errorf("read %s: %w", path, err)
	}
	return out, nil
}

// reconcileResume settles the stored transcript against what the adapter
// replayed, and is the reason resume is not simply "append from now on".
//
// session/load replays the conversation as notifications before it
// answers, so by the time we get here the in-memory transcript has been
// rebuilt from the adapter — under a NEW roster key, with seq restarting
// at 1. The file, meanwhile, holds the previous run's seqs. Appending
// into that would interleave two numbering schemes and the fold on load
// would silently overwrite real history.
//
// So: whichever record is richer wins, memory and file are made to agree,
// and the file is rewritten from the result.
//
//   - adapter replayed at least as much as we stored → its version is
//     authoritative (it is the agent's own state) and the file is
//     rewritten from memory.
//   - adapter replayed less (some replay nothing at all) → OUR record is
//     the better one. Memory is refilled from disk and the seq counter
//     continues past it, so the window shows the whole conversation and
//     new events keep numbering where the old ones stopped.
//
// The second case is transcript replay doing real work: a session the
// agent has half-forgotten still comes back whole.
func reconcileResume(key, sessionID string, now time.Time) {
	if key == "" || sessionID == "" {
		return
	}
	stored, err := loadTranscript(sessionID)
	if err != nil {
		log.Printf("agentd: transcript load session=%s: %v", sessionID, err)
	}

	transMu.Lock()
	t := trans[key]
	if t == nil {
		t = newTranscript(key)
		trans[key] = t
	}
	replayed := len(t.events)
	if replayed < len(stored) {
		t.events = append([]Event(nil), stored...)
		t.toolAt = map[string]int{}
		t.openMessage = -1
		var max uint64
		for i, e := range t.events {
			if e.Seq > max {
				max = e.Seq
			}
			if e.Kind == EventTool && e.ToolID != "" {
				t.toolAt[e.ToolID] = i
			}
		}
		t.seq = max
	}
	events := append([]Event(nil), t.events...)
	transMu.Unlock()

	log.Printf("agentd: transcript reconciled key=%s session=%s replayed=%d stored=%d kept=%d",
		key, sessionID, replayed, len(stored), len(events))
	logInFlight(key, sessionID, stored, events, now)

	// Bind before rewriting so subsequent live events persist, and
	// rewrite so the file matches what the window is showing.
	storeMu.Lock()
	storeSession[key] = sessionID
	storeMu.Unlock()
	if err := rewriteTranscript(sessionID, events, now); err != nil {
		log.Printf("agentd: transcript rewrite session=%s: %v", sessionID, err)
	}
}

// rewriteTranscript replaces a session's file with exactly these events.
// Temp-and-rename, so a reader either sees the old complete file or the
// new one — never a half-written conversation. This is the one path that
// does not append; it exists because resume has to reconcile two
// numbering schemes into one.
func rewriteTranscript(sessionID string, events []Event, now time.Time) error {
	path := transcriptPath(sessionID)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Drain first, and make the writer let go of the file: queued appends
	// belong to the copy we are about to replace, and an fd still open on
	// the old inode would take every later event with it.
	closeTranscriptFile(sessionID)
	waitForTranscriptWrites()

	tmp, err := os.CreateTemp(filepath.Dir(path), ".transcript-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	w := bufio.NewWriter(tmp)
	meta, _ := json.Marshal(transcriptMeta{
		Kind: metaKind, Version: transcriptVer, SessionID: sessionID, StartedMS: now.UnixMilli(),
	})
	w.Write(meta)
	w.WriteByte('\n')
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			continue
		}
		w.Write(line)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// waitForTranscriptWrites blocks until the writer has drained and
// flushed. Used by rewrite (which must not race queued appends) and by
// tests, which would otherwise have to sleep.
func waitForTranscriptWrites() {
	storeMu.Lock()
	started := storeCh != nil
	storeMu.Unlock()
	if !started {
		return
	}
	deadline := time.After(5 * time.Second)
	for {
		if len(storeCh) == 0 {
			// The queue is empty, but the writer may still be mid-batch;
			// one idle signal after that point means it has flushed.
			select {
			case <-storeIdle:
			case <-time.After(20 * time.Millisecond):
			}
			if len(storeCh) == 0 {
				return
			}
		}
		select {
		case <-deadline:
			log.Printf("agentd: transcript writer did not drain in time")
			return
		case <-storeIdle:
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// ── session metadata ────────────────────────────────────────────────────
//
// The meta header names a session at its birth, when almost nothing is
// known: not the model (the agent's settings arrive just after), not the
// title (the agent names its own work once it has worked out what the
// work is), and obviously not how it ended.
//
// So metadata is a SUMMARY record appended like any other line, and the
// last one in the file wins. Appending rather than rewriting the header
// is what keeps the format append-only; writing it more than once is
// what lets a session that was killed with the router still carry the
// model it was using, instead of only sessions lucky enough to retire
// cleanly.
type transcriptSummary struct {
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Dir       string `json:"dir,omitempty"`
	// Title is the agent's own name for the work. "codex · wash" tells
	// you nothing a week later; "Fix the reconnect banner race" does.
	Title string `json:"title,omitempty"`
	// Events is the folded event count, known only when we write this at
	// the end. Zero means "not counted yet", not "empty".
	Events int `json:"events,omitempty"`
	// EndedMS is set only by the final summary. Its absence is how the
	// index tells a session that ended from one the router outlived.
	EndedMS int64 `json:"ended_ms,omitempty"`
	// EndReason is why it stopped: retired by the user, or unknown when
	// the process went away without saying.
	EndReason string `json:"end_reason,omitempty"`
	AtMS      int64  `json:"at_ms,omitempty"`
}

const summaryKind = "summary"

// writeSummary appends one summary record for a session.
func writeSummary(sessionID string, s transcriptSummary) {
	if sessionID == "" {
		return
	}
	s.Kind = summaryKind
	s.SessionID = sessionID
	line, err := json.Marshal(s)
	if err != nil {
		return
	}
	enqueue(sessionID, line)
}

// SessionMeta is what the history panel lists. Assembled from a
// transcript's head and tail without reading the conversation in
// between — a history list must not cost the sum of every transcript.
type SessionMeta struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent,omitempty"`
	Model     string `json:"model,omitempty"`
	Cwd       string `json:"cwd,omitempty"`
	Dir       string `json:"dir,omitempty"`
	Title     string `json:"title,omitempty"`
	StartedMS int64  `json:"started_ms,omitempty"`
	EndedMS   int64  `json:"ended_ms,omitempty"`
	EndReason string `json:"end_reason,omitempty"`
	Events    int    `json:"events,omitempty"`
	// Bytes is the transcript's size on disk, so the UI can say what
	// history costs and offer to prune the expensive ones.
	Bytes int64 `json:"bytes,omitempty"`
	// Snippet is the line that matched, with a little either side. Absent
	// when the query matched metadata instead (the row already shows the
	// title and directory, so quoting them back is noise) or when there
	// was no query at all.
	Snippet string `json:"snippet,omitempty"`
	// Live / Detached / RowKey are stamped on the way out from the
	// roster, not read from the file — the transcript index knows what a
	// session WAS, and only the roster knows what it is doing now.
	//
	// The panel did not have these at all, so it would happily offer to
	// resume a session that was already running: the precise duplication
	// the menu's filter exists to prevent, in the view that had no
	// filter. Same predicate, both views (see rosterIndex).
	Live     bool   `json:"live,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	RowKey   string `json:"row_key,omitempty"`
}

// summaryTailBytes is how much of the end of a file is scanned for the
// last summary record. Summaries are small and written last, so a few KB
// finds them; a session whose final line is a huge inline image may push
// its summary out of this window, which costs metadata, never events.
const summaryTailBytes = 8 << 10

// readSessionMeta assembles one session's metadata from its file's head
// and tail. Cheap by construction: two small reads, no matter how long
// the conversation is.
func readSessionMeta(path string) (SessionMeta, bool) {
	f, err := os.Open(path)
	if err != nil {
		return SessionMeta{}, false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return SessionMeta{}, false
	}

	var out SessionMeta
	out.Bytes = fi.Size()

	// Head: the meta line names the session.
	head := bufio.NewScanner(f)
	head.Buffer(make([]byte, 0, 8<<10), 1<<20)
	if head.Scan() {
		var m transcriptMeta
		if err := json.Unmarshal(head.Bytes(), &m); err == nil && m.Kind == metaKind {
			out.SessionID = m.SessionID
			out.Agent = m.Agent
			out.Cwd = m.Cwd
			out.Dir = dirLabel(m.Cwd)
			out.StartedMS = m.StartedMS
		}
	}
	if out.SessionID == "" {
		return SessionMeta{}, false
	}

	// Tail: the last summary wins, and the last event dates the session
	// when no summary was ever written (the router died mid-conversation).
	start := fi.Size() - summaryTailBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, 0); err != nil {
		return out, true
	}
	tail := bufio.NewScanner(f)
	tail.Buffer(make([]byte, 0, 8<<10), 1<<20)
	if start > 0 {
		tail.Scan() // discard the partial first line
	}
	var lastEventAt int64
	for tail.Scan() {
		b := tail.Bytes()
		if len(b) == 0 {
			continue
		}
		var probe struct {
			Kind string `json:"kind"`
			AtMS int64  `json:"at_ms"`
		}
		if json.Unmarshal(b, &probe) != nil {
			continue
		}
		if probe.Kind != summaryKind {
			if probe.AtMS > lastEventAt {
				lastEventAt = probe.AtMS
			}
			continue
		}
		var s transcriptSummary
		if json.Unmarshal(b, &s) != nil {
			continue
		}
		// Later summaries overwrite earlier ones field by field, but only
		// where they actually say something: the start-of-session summary
		// carries the model, the final one carries the ending, and a
		// blank field in the later record must not erase the earlier.
		if s.Agent != "" {
			out.Agent = s.Agent
		}
		if s.Model != "" {
			out.Model = s.Model
		}
		if s.Title != "" {
			out.Title = s.Title
		}
		if s.Cwd != "" {
			out.Cwd, out.Dir = s.Cwd, dirLabel(s.Cwd)
		}
		if s.Dir != "" {
			out.Dir = s.Dir
		}
		if s.Events > 0 {
			out.Events = s.Events
		}
		if s.EndedMS > 0 {
			out.EndedMS = s.EndedMS
		}
		if s.EndReason != "" {
			out.EndReason = s.EndReason
		}
	}
	// A session with no final summary still gets a last-activity time, so
	// history can sort it sensibly instead of stacking every crashed
	// session at the epoch.
	if out.EndedMS == 0 && lastEventAt > 0 {
		out.EndedMS = lastEventAt
	}
	return out, true
}

// listSessionMeta is the history index: every stored transcript, newest
// activity first.
func listSessionMeta() []SessionMeta {
	dir := transcriptDir()
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	out := make([]SessionMeta, 0, len(ents))
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if m, ok := readSessionMeta(filepath.Join(dir, e.Name())); ok {
			out = append(out, m)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return sessionRecency(out[i]) > sessionRecency(out[j])
	})
	return out
}

func sessionRecency(m SessionMeta) int64 {
	if m.EndedMS > 0 {
		return m.EndedMS
	}
	return m.StartedMS
}

// searchTranscript reports whether a stored conversation contains every
// term, case-insensitively, and returns the line that proves it. The
// point of searching history is that you remember what a session was
// ABOUT, not what it was called — "that one where I was chasing the
// reconnect race" is a content query, and title matching alone would
// miss it.
//
// All terms must appear, but not in one line and not in order: a
// conversation is the unit, so "reconnect race" finds the session that
// discussed both even if the two words are ten minutes apart. The
// snippet comes from the FIRST term to match anywhere, which is the line
// most likely to be the one being remembered.
//
// Streamed line by line so a long conversation costs a buffer rather
// than its whole size in memory. Unlike the old single-term version it
// cannot stop at the first hit — "did every term appear?" is only
// answerable at the end — but it stops as soon as the last outstanding
// term lands.
func searchTranscript(sessionID string, terms []string) (string, bool) {
	if len(terms) == 0 {
		return "", true
	}
	path := transcriptPath(sessionID)
	if path == "" {
		return "", false
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	need := make(map[string]bool, len(terms))
	for _, t := range terms {
		need[strings.ToLower(t)] = true
	}
	snippet := ""
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), maxImageBytes+(1<<16))
	for sc.Scan() {
		// Match on the decoded text, not the raw line: a JSON line
		// carries escapes and base64 image data, and searching that
		// finds nothing a human typed and plenty they didn't.
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if e.Kind == EventImage {
			continue // Text is base64 here
		}
		for _, field := range [2]string{e.Text, e.Title} {
			if field == "" {
				continue
			}
			low := strings.ToLower(field)
			for t := range need {
				at := strings.Index(low, t)
				if at < 0 {
					continue
				}
				delete(need, t)
				if snippet == "" {
					snippet = excerpt(field, at, len(t))
				}
			}
		}
		if len(need) == 0 {
			return snippet, true
		}
	}
	return "", false
}

// snippetRadius is how much of the line either side of a hit rides along.
// Enough to place the phrase in a sentence, short enough for a list row.
const snippetRadius = 60

// excerpt lifts a readable window around a match, with ellipses where it
// cut. Byte offsets from the caller's Index, so the cut points are nudged
// off multi-byte runes rather than splitting them into mojibake.
func excerpt(s string, at, n int) string {
	start := at - snippetRadius
	if start < 0 {
		start = 0
	}
	end := at + n + snippetRadius
	if end > len(s) {
		end = len(s)
	}
	for start > 0 && !utf8.RuneStart(s[start]) {
		start--
	}
	for end < len(s) && !utf8.RuneStart(s[end]) {
		end++
	}
	out := strings.TrimSpace(collapseSpace(s[start:end]))
	if start > 0 {
		out = "…" + out
	}
	if end < len(s) {
		out += "…"
	}
	return out
}

// collapseSpace folds newlines and runs of whitespace into single spaces:
// a transcript line can be a code block, and a snippet is one line in a
// list.
func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// queryTerms splits a query the way a person means it: whitespace
// separated, all of them required. Empty query = no terms = match all.
func queryTerms(q string) []string {
	return strings.Fields(strings.ToLower(q))
}

// matchesMeta is the cheap half of a history query: the fields already in
// the index. Tried before opening the transcript, so a search for an
// agent or a directory never reads a conversation at all.
func matchesMeta(m SessionMeta, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	fields := [...]string{m.Title, m.Agent, m.Model, m.Dir, m.Cwd, m.SessionID}
	for _, t := range terms {
		hit := false
		for _, f := range fields {
			if f != "" && strings.Contains(strings.ToLower(f), t) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return true
}

// historyQuery lists stored sessions, newest first, optionally filtered.
// limit <= 0 means every match.
func historyQuery(q string, limit int) []SessionMeta {
	all := listSessionMeta()
	terms := queryTerms(q)
	if len(terms) == 0 {
		if limit > 0 && len(all) > limit {
			all = all[:limit]
		}
		return all
	}
	// The index says who COULD match, so the confirm pass below opens a
	// handful of transcripts instead of all of them. ok=false means it
	// can't narrow this query (a term shorter than a trigram), and every
	// session is a candidate — correct, just not accelerated.
	cand, narrowed := searchCandidates(terms)
	out := make([]SessionMeta, 0, len(all))
	for _, m := range all {
		// Metadata first: it is already in hand, and a query that matches
		// there saves reading the conversation — and matters more now,
		// because metadata is NOT in the trigram index, so a session
		// whose only match is its title would be narrowed away.
		if matchesMeta(m, terms) {
			out = append(out, m)
		} else {
			if narrowed && !cand[m.SessionID] {
				continue
			}
			snip, ok := searchTranscript(m.SessionID, terms)
			if !ok {
				continue
			}
			m.Snippet = snip
			out = append(out, m)
		}
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

// logInFlight says out loud whether this session was mid-turn when it
// died, and whether the resume got that turn back.
//
// This is the distinction that matters and the one nothing logged. A
// disconnect is survivable if the agent happened to be idle when you
// walked away and destructive if it was mid-task — which is backwards,
// since mid-task is exactly when walking away is the point. Until the
// difference is stated, "your agent came back" and "your agent came back
// without the thing it was doing" look identical in the log.
//
// A turn was in flight if the last thing the stored transcript knows
// about is unfinished: a tool call still pending or in progress, or the
// human's prompt with nothing after it. Recovery means the agent's own
// replay produced something past that point; anything else is a drop.
func logInFlight(key, sessionID string, stored, replayed []Event, now time.Time) {
	tool, at := lastUnfinished(stored)
	if at < 0 {
		log.Printf("agentd: resume in-flight key=%s session=%s state=idle-at-exit — nothing was running, nothing to lose", key, sessionID)
		return
	}
	age := "unknown"
	if ms := stored[at].AtMS; ms > 0 {
		age = now.Sub(time.UnixMilli(ms)).Round(time.Second).String()
	}
	if _, stillAt := lastUnfinished(replayed); stillAt < 0 {
		log.Printf("agentd: resume in-flight key=%s session=%s state=recovered what=%q age=%s — the agent replayed past it",
			key, sessionID, tool, age)
		return
	}
	log.Printf("agentd: resume in-flight key=%s session=%s state=DROPPED what=%q age=%s — the turn was executing at exit and did not come back",
		key, sessionID, tool, age)
}

// lastUnfinished returns a label for the trailing unfinished work in evs
// and its index, or ("", -1) when the transcript ends cleanly.
func lastUnfinished(evs []Event) (string, int) {
	for i := len(evs) - 1; i >= 0; i-- {
		e := evs[i]
		switch e.Kind {
		case EventTool:
			if e.Status == acp.ToolStatusPending || e.Status == acp.ToolStatusInProgress {
				label := e.Title
				if label == "" {
					label = e.ToolKind
				}
				return "tool: " + label, i
			}
			return "", -1
		case EventUser:
			// A prompt with nothing after it: the agent was asked and
			// never answered.
			t := e.Text
			if len(t) > 60 {
				t = t[:60] + "…"
			}
			return "prompt: " + t, i
		case EventMessage, EventThought, EventTerminal, EventImage:
			// The agent said something after the prompt, so whatever was
			// in flight got at least partway out.
			return "", -1
		}
	}
	return "", -1
}
