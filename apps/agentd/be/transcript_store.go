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
	"strings"
	"sync"
	"time"
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

// listTranscripts returns the session ids that have a stored transcript.
func listTranscripts() []string {
	dir := transcriptDir()
	if dir == "" {
		return nil
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		// The file is named by the SANITISED id, so the id is read from
		// the meta line rather than inferred from the name.
		if id := metaSessionID(filepath.Join(dir, e.Name())); id != "" {
			out = append(out, id)
		}
	}
	return out
}

func metaSessionID(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return ""
	}
	var m transcriptMeta
	if err := json.Unmarshal(sc.Bytes(), &m); err != nil || m.Kind != metaKind {
		return ""
	}
	return m.SessionID
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
