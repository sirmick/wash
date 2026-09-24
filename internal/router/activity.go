package router

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/sirmick/wash/internal/activity"
	"github.com/sirmick/wash/pkg/wire"
)

// The activity journal (docs/COMMANDER.md §3) lives in the router because
// the router already witnesses most of the timeline on its frame path.
// This file is the glue: where the router's own facts are appended, how
// an app's activity.note is attested and bounded, and the four shell
// verbs a Timeline reads through. The store itself is internal/activity
// and never calls anything back.

// ActivityDir resolves where a host's journal lives:
// $XDG_STATE_HOME/wash/activity, else ~/.local/state/wash/activity, else
// "" (no journal — memory-only would be a journal that lies about
// yesterday).
func ActivityDir() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "wash", "activity")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".local", "state", "wash", "activity")
}

// openJournal opens the store the config asks for, or returns nil (the
// disabled journal) when it is off or has nowhere to live.
func openJournal(cfg Config, log Logger) *activity.Store {
	if cfg.NoActivity || cfg.ActivityDir == "" {
		return nil
	}
	s, err := activity.Open(cfg.ActivityDir, activity.Options{
		Retention: cfg.ActivityRetention,
		MaxBytes:  cfg.ActivityMaxBytes,
	})
	if err != nil {
		log("activity: journal off: %v", err)
		return nil
	}
	log("activity: journal at %s", cfg.ActivityDir)
	return s
}

// CloseJournal flushes and stops the journal; the runner defers it so a
// clean shutdown keeps the entries of its last quarter second.
func (r *Router) CloseJournal() {
	if r.journal != nil {
		r.journal.Close()
	}
}

// note appends one of the router's own facts. Nil-safe: with the journal
// off this is a no-op, which is the whole cost of --no-activity.
func (r *Router) note(e activity.Entry) {
	if r.journal == nil {
		return
	}
	r.journal.Append(e)
}

// noteWindow appends a window fact for inst's window win.
func (r *Router) noteWindow(kind string, inst *AppInstance, win uint32, title, line string, intent *wire.ActivityIntent) {
	if r.journal == nil || inst == nil {
		return
	}
	if title == "" {
		title = r.winSession.title(win)
	}
	if intent == nil {
		intent = &wire.ActivityIntent{Kind: "focus", AppID: inst.AppID, InstanceID: inst.InstanceID, WindowID: win}
	}
	r.journal.Append(activity.Entry{
		Kind: kind, App: inst.AppID, Instance: inst.InstanceID, Window: win,
		Title: title, Line: line, Intent: intent,
	})
}

// --- app notes ---

// noteRate bounds a chatty instance: a burst of noteBurst, refilled at
// noteRefill per second. An agent that notes every streamed chunk must not
// be able to write the journal full; the excess is dropped and counted in
// the log, never fatal.
const (
	noteBurst  = 30
	noteRefill = 5.0
)

type noteLimiter struct {
	mu     sync.Mutex
	tokens float64
	at     time.Time
}

func (l *noteLimiter) allow(now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at.IsZero() {
		l.tokens, l.at = noteBurst, now
	}
	l.tokens += now.Sub(l.at).Seconds() * noteRefill
	if l.tokens > noteBurst {
		l.tokens = noteBurst
	}
	l.at = now
	if l.tokens < 1 {
		return false
	}
	l.tokens--
	return true
}

// handleActivityNote records a fact an app states about itself. The
// router stamps app and instance from the attested sender; the payload
// cannot name another app, and a window it names must be its own.
func (inst *AppInstance) handleActivityNote(m wire.EvtActivityNote) error {
	r := inst.router
	if r.journal == nil {
		return nil
	}
	if !inst.Manifest.HasCapability(CapActivityNote) {
		r.log("activity: note refused app=%s instance=%s: lacks CapActivityNote", inst.AppID, inst.InstanceID)
		return nil
	}
	if m.Win != 0 && !inst.ownsWindow(m.Win) {
		r.log("activity: note refused app=%s instance=%s: win=%d not owned", inst.AppID, inst.InstanceID, m.Win)
		return nil
	}
	if !inst.notes.allow(time.Now()) {
		r.log("activity: note dropped app=%s instance=%s kind=%s: rate", inst.AppID, inst.InstanceID, m.Kind)
		return nil
	}
	if m.Kind == "" {
		m.Kind = "note"
	}
	if m.Intent != nil && m.Intent.AppID == "" {
		m.Intent.AppID = inst.AppID
	}
	r.journal.Append(activity.Entry{
		Kind: m.Kind, App: inst.AppID, Instance: inst.InstanceID, Window: m.Win,
		Title: m.Title, Line: m.Line, Ref: m.Ref, Intent: m.Intent,
	})
	return nil
}

// --- shell verbs ---

func (s *ShellSession) handleActivityQuery(m wire.ShellActivityQuery) error {
	j := s.router.journal
	if j == nil {
		return s.WriteCtrl(wire.NewShellActivityQueryErr(m.ReqID, wire.ErrCodeNotFound, "activity journal is off"))
	}
	// Off the dispatch loop: a query reads day files, and the shell's
	// input must not wait on a disk (the same rule as asset streaming).
	go func() {
		res := j.Query(activity.Query{
			From: m.From, To: m.To, Kinds: m.Kinds, Apps: m.Apps, Text: m.Text, Limit: m.Limit, Cursor: m.Cursor,
		})
		_ = s.WriteCtrl(wire.NewShellActivityQueryOK(m.ReqID, res.Entries, res.Cursor))
	}()
	return nil
}

func (s *ShellSession) handleActivityStats(m wire.ShellActivityStats) error {
	j := s.router.journal
	go func() {
		_ = s.WriteCtrl(wire.NewShellActivityStatsOK(m.ReqID, j.Stats()))
	}()
	return nil
}

func (s *ShellSession) handleActivityClear(m wire.ShellActivityClear) error {
	j := s.router.journal
	go func() {
		if err := j.Clear(); err != nil {
			s.router.log("activity: clear: %v", err)
		} else {
			s.router.log("activity: cleared by conn=%d", s.connID)
		}
		_ = s.WriteCtrl(wire.NewShellActivityClearOK(m.ReqID))
	}()
	return nil
}

// handleActivityTail starts or stops pushing new entries to this shell.
// One tail per connection; entries ride the telemetry class like
// link.stats, so a Timeline stays live without polling and a slow link
// loses a row rather than stalling input.
func (s *ShellSession) handleActivityTail(m wire.ShellActivityTail) error {
	s.tailMu.Lock()
	defer s.tailMu.Unlock()
	if s.tailStop != nil {
		s.tailStop()
		s.tailStop = nil
	}
	if !m.On || s.router.journal == nil {
		return nil
	}
	ch, cancel := s.router.journal.Tail(256)
	s.tailStop = cancel
	go func() {
		for {
			select {
			case e, ok := <-ch:
				if !ok {
					return
				}
				data, err := wire.EncodeCtrl(wire.NewShellActivityEntry(e))
				if err != nil {
					continue
				}
				f := wire.Frame{Flags: wire.FlagEnd, Channel: ChannelControl, Payload: data}.WithClass(telemetryClass)
				s.scheduler.SubmitTelemetry(f)
			case <-s.drainerDone:
				cancel()
				return
			}
		}
	}()
	return nil
}

// stopActivityTail is called on shell teardown.
func (s *ShellSession) stopActivityTail() {
	s.tailMu.Lock()
	if s.tailStop != nil {
		s.tailStop()
		s.tailStop = nil
	}
	s.tailMu.Unlock()
}
