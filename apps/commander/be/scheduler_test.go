package commander

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/pkg/inference"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// world is the fake desktop the scheduler looks at: a roster, per-window
// observations, a provider that answers briefs from a script, and the
// journal it writes to.
type world struct {
	shells   int
	roster   []wire.ObserveRosterEntry
	obs      map[string]wire.Observation
	info     ProviderInfo
	infoErr  error
	requests []inference.Request
	answer   func(req inference.Request) string
	notes    []wire.EvtActivityNote
	now      time.Time
	logs     []string
}

func (w *world) Generate(_ context.Context, req inference.Request) (inference.Result, error) {
	w.requests = append(w.requests, req)
	return inference.Result{Text: w.answer(req), Provider: "fake", Model: "m"}, nil
}

func (w *world) deps() deps {
	return deps{
		roster: func(context.Context) (sdk.Roster, error) {
			return sdk.Roster{Shells: w.shells, Instances: w.roster}, nil
		},
		observe:  func(_ context.Context, id string) (wire.Observation, error) { return w.obs[id], nil },
		gen:      w,
		provider: func(context.Context) (ProviderInfo, error) { return w.info, w.infoErr },
		note:     func(n wire.EvtActivityNote) error { w.notes = append(w.notes, n); return nil },
		now:      func() time.Time { return w.now },
		logf:     func(f string, a ...any) { w.logs = append(w.logs, fmt.Sprintf(f, a...)) },
	}
}

// echoBriefs answers a batch with one brief per source whose goal is the
// source's content, so a test can see what was sent and what was said.
func echoBriefs(req inference.Request) string {
	var in struct {
		Sources []struct {
			Index   int    `json:"index"`
			Content string `json:"content"`
		} `json:"sources"`
	}
	if err := json.Unmarshal([]byte(req.Input[0].Text), &in); err == nil && len(in.Sources) > 0 {
		var out []map[string]any
		for _, s := range in.Sources {
			out = append(out, map[string]any{"index": s.Index, "goal": "goal:" + s.Content, "state": "active"})
		}
		b, _ := json.Marshal(out)
		return string(b)
	}
	// A single source is a plain brief request.
	var src struct {
		Content string `json:"content"`
	}
	_ = json.Unmarshal([]byte(req.Input[0].Text), &src)
	b, _ := json.Marshal(map[string]any{"goal": "goal:" + src.Content, "state": "active"})
	return string(b)
}

func newWorld() *world {
	w := &world{
		shells: 1,
		roster: []wire.ObserveRosterEntry{
			{App: "com.wash.term", InstanceID: "t1", WindowID: 1, Title: "~/wash", Eligible: true},
			{App: "com.wash.edit", InstanceID: "e1", WindowID: 2, Title: "a.go", Eligible: true},
			{App: "com.wash.priv", InstanceID: "p1", WindowID: 3, Title: "priv"},
		},
		obs: map[string]wire.Observation{
			"t1": {Source: "pty-tail", Revision: "pty:1:100", Content: "$ make test\nok"},
			"e1": {Source: "app-state", Revision: "state:1", Content: `{"tabs":["a.go"]}`},
			"p1": {Source: "none"},
		},
		info:   ProviderInfo{Connection: "ollama", Adapter: "openai", Local: true, Available: true},
		answer: echoBriefs,
		now:    time.Unix(1_800_000_000, 0),
	}
	return w
}

func on() Settings {
	return Settings{Automatic: true, IntervalSec: 60, BudgetPerHour: 10, BatchMax: 6}
}

// Off means nothing is looked at; on means every eligible window is, the
// changed ones go in one batch, and each brief becomes a journal entry.
func TestTickObservesBatchesAndJournals(t *testing.T) {
	w := newWorld()
	s := newScheduler(w.deps(), Settings{})
	if rep := s.tick(context.Background()); rep.skipped != "off" || rep.observed != 0 || len(w.requests) != 0 {
		t.Fatalf("off tick did work: %+v", rep)
	}

	s.setSettings(on())
	rep := s.tick(context.Background())
	if rep.observed != 2 || rep.changed != 2 || rep.requests != 1 || rep.briefs != 2 || rep.skipped != "" {
		t.Fatalf("first tick: %+v", rep)
	}
	if len(w.notes) != 2 || w.notes[0].Kind != "brief" || w.notes[0].Title != "~/wash" || !strings.HasPrefix(w.notes[0].Line, "active · goal:$ make test") {
		t.Fatalf("notes=%+v", w.notes)
	}
	n := w.notes[0]
	if n.Win != 0 || n.Intent == nil || n.Intent.Kind != "focus" || n.Intent.WindowID != 1 || n.Intent.AppID != "com.wash.term" {
		t.Fatalf("intent=%+v win=%d", n.Intent, n.Win)
	}
	if n.Ref["source"] != "pty-tail" || n.Ref["revision"] != "pty-tail:pty:1:100" || n.Ref["provider"] != "fake" {
		t.Fatalf("ref=%+v", n.Ref)
	}
	// The ineligible window was never observed, let alone sent.
	if strings.Contains(w.requests[0].Input[0].Text, "priv") {
		t.Fatal("an ineligible window reached the provider")
	}
	st := s.state()
	if !st.Stats.Running || st.Stats.Briefs != 2 || st.Stats.Requests != 1 || st.Stats.Provider != "ollama" {
		t.Fatalf("stats=%+v", st.Stats)
	}
}

// Nothing moved: no request. Something moved but the brief reads the
// same: no journal entry. Something moved and the brief changed: one.
func TestTickDedupsByRevisionThenByBrief(t *testing.T) {
	w := newWorld()
	s := newScheduler(w.deps(), on())
	s.tick(context.Background())
	if len(w.requests) != 1 || len(w.notes) != 2 {
		t.Fatalf("setup: requests=%d notes=%d", len(w.requests), len(w.notes))
	}

	rep := s.tick(context.Background())
	if rep.changed != 0 || rep.requests != 0 || len(w.requests) != 1 {
		t.Fatalf("unchanged windows were sent again: %+v", rep)
	}

	// The terminal printed a blank line: new revision, same content-ish,
	// and the model says the same thing → deduped, no entry.
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "pty:1:101", Content: "$ make test\nok"}
	rep = s.tick(context.Background())
	if rep.changed != 1 || rep.requests != 1 || rep.briefs != 0 || rep.deduped != 1 || len(w.notes) != 2 {
		t.Fatalf("same brief was journaled: %+v notes=%d", rep, len(w.notes))
	}

	// Now it really did something.
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "pty:1:200", Content: "$ git push\ndone"}
	rep = s.tick(context.Background())
	if rep.briefs != 1 || len(w.notes) != 3 || !strings.Contains(w.notes[2].Line, "git push") {
		t.Fatalf("a changed brief was not journaled: %+v", rep)
	}

	// An observation with no revision is keyed by its content.
	w.roster = append(w.roster, wire.ObserveRosterEntry{App: "com.wash.about", InstanceID: "a1", WindowID: 4, Eligible: true})
	w.obs["a1"] = wire.Observation{Source: "dom", Content: "About wash"}
	if rep := s.tick(context.Background()); rep.changed != 1 || rep.briefs != 1 {
		t.Fatalf("dom window: %+v", rep)
	}
	if rep := s.tick(context.Background()); rep.changed != 0 {
		t.Fatalf("same dom content was sent again: %+v", rep)
	}
	// A window that closed is forgotten, so its return is news again.
	w.roster = w.roster[:3]
	s.tick(context.Background())
	w.roster = append(w.roster, wire.ObserveRosterEntry{App: "com.wash.about", InstanceID: "a1", WindowID: 4, Eligible: true})
	if rep := s.tick(context.Background()); rep.changed != 1 {
		t.Fatalf("a reopened window was not observed afresh: %+v", rep)
	}
}

// The gates: a hosted provider needs its switch, nobody attached means
// nobody is briefed, and the hourly budget is a hard stop.
func TestTickGates(t *testing.T) {
	w := newWorld()
	w.info = ProviderInfo{Connection: "openai", Adapter: "openai", Local: false, Available: true}
	s := newScheduler(w.deps(), on())
	if rep := s.tick(context.Background()); !strings.Contains(rep.skipped, "hosted") || len(w.requests) != 0 || s.state().Stats.Running {
		t.Fatalf("hosted provider was used: %+v", rep)
	}
	set := on()
	set.Hosted = true
	s.setSettings(set)
	w.shells = 0
	if rep := s.tick(context.Background()); rep.skipped != "nobody attached" || len(w.requests) != 0 {
		t.Fatalf("briefed an empty seat: %+v", rep)
	}
	w.shells = 1
	if rep := s.tick(context.Background()); rep.requests != 1 {
		t.Fatalf("hosted+allowed did not run: %+v", rep)
	}

	w.info.Available = false
	if rep := s.tick(context.Background()); !strings.HasPrefix(rep.skipped, "provider unavailable") || s.state().Stats.Running {
		t.Fatalf("unavailable provider: %+v", rep)
	}
	w.info.Available = true
	w.infoErr = errors.New("no service")
	if rep := s.tick(context.Background()); rep.errors != 1 || !strings.Contains(s.state().Stats.LastError, "no service") {
		t.Fatalf("provider error: %+v", rep)
	}
	w.infoErr = nil

	// Budget: two requests per hour, each window in its own batch. A
	// fresh hour first — the tick above already spent one.
	w.now = w.now.Add(time.Hour)
	set.BudgetPerHour, set.BatchMax = 2, 1
	s.setSettings(set)
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "r2", Content: "two"}
	w.obs["e1"] = wire.Observation{Source: "app-state", Revision: "r2", Content: "two"}
	before := len(w.requests)
	if rep := s.tick(context.Background()); rep.requests != 2 || len(w.requests) != before+2 {
		t.Fatalf("batch of one: %+v", rep)
	}
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "r3", Content: "three"}
	if rep := s.tick(context.Background()); rep.requests != 0 || rep.skipped != "hourly budget spent" {
		t.Fatalf("budget not enforced: %+v", rep)
	}
	w.now = w.now.Add(time.Hour)
	if rep := s.tick(context.Background()); rep.requests != 1 {
		t.Fatalf("budget did not roll over: %+v", rep)
	}
}

// Quiet windows lengthen the wait, up to 4×; a change resets it. A batch
// is split by bytes as well as by count.
func TestBackoffAndByteBatching(t *testing.T) {
	w := newWorld()
	s := newScheduler(w.deps(), on())
	s.tick(context.Background())
	if s.backoff != 1 {
		t.Fatalf("backoff after a change = %d", s.backoff)
	}
	for i, want := range []int{2, 4, 4} {
		s.tick(context.Background())
		if s.backoff != want {
			t.Fatalf("quiet tick %d: backoff=%d want %d", i, s.backoff, want)
		}
	}
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "x", Content: "new"}
	s.tick(context.Background())
	if s.backoff != 1 {
		t.Fatalf("backoff did not reset on a change: %d", s.backoff)
	}

	big := strings.Repeat("x", batchBytes-10)
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "y", Content: big}
	w.obs["e1"] = wire.Observation{Source: "app-state", Revision: "y", Content: big}
	before := len(w.requests)
	if rep := s.tick(context.Background()); rep.requests != 2 || len(w.requests) != before+2 {
		t.Fatalf("two large windows should be two requests: %+v", rep)
	}
}

// A provider that answers nonsense costs one error, and the same content
// is not retried until it changes.
func TestBadAnswerIsNotRetriedUntilContentMoves(t *testing.T) {
	w := newWorld()
	w.answer = func(inference.Request) string { return "I cannot help with that." }
	s := newScheduler(w.deps(), on())
	rep := s.tick(context.Background())
	if rep.errors != 1 || rep.briefs != 0 || len(w.requests) != 2 { // one + the repair
		t.Fatalf("bad answer: %+v requests=%d", rep, len(w.requests))
	}
	if rep := s.tick(context.Background()); rep.requests != 0 {
		t.Fatalf("retried unchanged content: %+v", rep)
	}
	w.answer = echoBriefs
	w.obs["t1"] = wire.Observation{Source: "pty-tail", Revision: "z", Content: "again"}
	if rep := s.tick(context.Background()); rep.briefs != 1 {
		t.Fatalf("change did not retry: %+v", rep)
	}
}

func TestSettingsNormalise(t *testing.T) {
	s := Settings{IntervalSec: 1, BatchMax: 99}.normalised()
	if s.IntervalSec != minInterval || s.BatchMax != maxBatch || s.BudgetPerHour != defaultBudget {
		t.Fatalf("normalised=%+v", s)
	}
	if d := defaultSettings(); d.Automatic || d.Hosted || d.IntervalSec != 300 {
		t.Fatalf("defaults=%+v", d)
	}
}
