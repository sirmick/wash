package commander

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sirmick/wash/pkg/inference/activity"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// The schedule (docs/COMMANDER.md §5.3): on a cadence, while someone is
// looking and the provider is allowed, observe every eligible window,
// brief the ones that changed — batched, not one call per window — and
// write each new brief to the journal. Three things keep it quiet:
//
//   - a window whose observation has not moved (revision, else a hash of
//     the content) is not sent again;
//   - a brief equal to the last one for that window is not journaled;
//   - a tick that finds nothing changed lengthens the next wait, up to
//     4× the cadence, and a per-hour request budget caps the rest.

// ProviderInfo is what the inference service says about the selected
// connection: enough to apply the on-box rule.
type ProviderInfo struct {
	Connection string `json:"connection"`
	Adapter    string `json:"adapter"`
	Model      string `json:"model,omitempty"`
	Local      bool   `json:"local"`
	Available  bool   `json:"available"`
	Detail     string `json:"detail,omitempty"`
}

// Stats is what the rail shows beside the switch: whether the schedule is
// running and why not, and what it has done this session.
type Stats struct {
	Running    bool   `json:"running"`
	Reason     string `json:"reason,omitempty"`
	Provider   string `json:"provider,omitempty"`
	Model      string `json:"model,omitempty"`
	Local      bool   `json:"local,omitempty"`
	LastTickAt int64  `json:"last_tick_at,omitempty"`
	NextTickAt int64  `json:"next_tick_at,omitempty"`
	Ticks      int    `json:"ticks"`
	Observed   int    `json:"observed"`
	Changed    int    `json:"changed"`
	Requests   int    `json:"requests"`
	Briefs     int    `json:"briefs"`
	Deduped    int    `json:"deduped"`
	Errors     int    `json:"errors"`
	LastError  string `json:"last_error,omitempty"`
}

// State is the published shape: settings and stats together.
type State struct {
	Settings Settings `json:"settings"`
	Stats    Stats    `json:"stats"`
}

// deps is everything the schedule touches outside itself, so a test can
// stand the whole thing up with fakes.
type deps struct {
	roster   func(ctx context.Context) (sdk.Roster, error)
	observe  func(ctx context.Context, instanceID string) (wire.Observation, error)
	gen      activity.Generator
	provider func(ctx context.Context) (ProviderInfo, error)
	note     func(n wire.EvtActivityNote) error
	publish  func(st State)
	now      func() time.Time
	logf     func(format string, args ...any)
}

type seen struct {
	key   string
	brief *activity.Brief
	at    time.Time
}

type scheduler struct {
	d deps

	mu       sync.Mutex
	settings Settings
	stats    Stats
	last     map[string]*seen
	hourAt   time.Time
	hourUsed int
	backoff  int
	wake     chan struct{}
}

func newScheduler(d deps, s Settings) *scheduler {
	if d.now == nil {
		d.now = time.Now
	}
	if d.logf == nil {
		d.logf = func(string, ...any) {}
	}
	if d.publish == nil {
		d.publish = func(State) {}
	}
	return &scheduler{d: d, settings: s.normalised(), last: map[string]*seen{}, backoff: 1, wake: make(chan struct{}, 1)}
}

// setSettings replaces the settings and wakes the loop so a switch takes
// effect now, not at the end of the current wait.
func (s *scheduler) setSettings(n Settings) {
	s.mu.Lock()
	s.settings = n.normalised()
	if !s.settings.Automatic {
		s.stats.Running, s.stats.Reason, s.stats.NextTickAt = false, "off", 0
	}
	s.backoff = 1
	st := State{Settings: s.settings, Stats: s.stats}
	s.mu.Unlock()
	s.d.publish(st)
	s.kick()
}

func (s *scheduler) state() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return State{Settings: s.settings, Stats: s.stats}
}

// kick asks the loop to tick as soon as it can.
func (s *scheduler) kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// run is the loop: wait the cadence (times the backoff), or a kick, then
// tick. Off means wait for a kick only.
func (s *scheduler) run(ctx context.Context) {
	for {
		s.mu.Lock()
		on := s.settings.Automatic
		wait := time.Duration(s.settings.IntervalSec) * time.Second * time.Duration(s.backoff)
		if on {
			s.stats.NextTickAt = s.d.now().Add(wait).UnixMilli()
		}
		s.mu.Unlock()
		var timer <-chan time.Time
		if on {
			t := time.NewTimer(wait)
			timer = t.C
			defer t.Stop()
		}
		select {
		case <-ctx.Done():
			return
		case <-timer:
		case <-s.wake:
		}
		s.tick(ctx)
	}
}

// report is what one tick did, for the log line and the tests.
type report struct {
	skipped  string
	observed int
	changed  int
	requests int
	briefs   int
	deduped  int
	errors   int
}

func (s *scheduler) tick(ctx context.Context) report {
	s.mu.Lock()
	set := s.settings
	s.mu.Unlock()
	rep := s.pass(ctx, set)

	s.mu.Lock()
	s.stats.Ticks++
	s.stats.LastTickAt = s.d.now().UnixMilli()
	s.stats.Observed += rep.observed
	s.stats.Changed += rep.changed
	s.stats.Requests += rep.requests
	s.stats.Briefs += rep.briefs
	s.stats.Deduped += rep.deduped
	s.stats.Errors += rep.errors
	if rep.skipped != "" {
		s.stats.Reason = rep.skipped
	} else {
		s.stats.Reason = ""
	}
	// Idle-aware: nothing changed, wait longer; something did, back to
	// the cadence.
	if rep.changed == 0 {
		if s.backoff < 4 {
			s.backoff *= 2
		}
	} else {
		s.backoff = 1
	}
	st := State{Settings: s.settings, Stats: s.stats}
	s.mu.Unlock()
	s.d.publish(st)
	s.d.logf("wash-commander: tick observed=%d changed=%d requests=%d briefs=%d deduped=%d errors=%d skipped=%q", rep.observed, rep.changed, rep.requests, rep.briefs, rep.deduped, rep.errors, rep.skipped)
	return rep
}

// pass is one look at everything: the gates, the roster, the
// observations, the batches, the journal.
func (s *scheduler) pass(ctx context.Context, set Settings) report {
	var rep report
	if !set.Automatic {
		s.setRunning(false)
		rep.skipped = "off"
		return rep
	}
	info, err := s.d.provider(ctx)
	if err != nil {
		s.fail(err)
		rep.errors++
		rep.skipped = "provider: " + err.Error()
		return rep
	}
	s.mu.Lock()
	s.stats.Provider, s.stats.Model, s.stats.Local = info.Connection, info.Model, info.Local
	s.mu.Unlock()
	if !info.Available {
		s.setRunning(false)
		rep.skipped = "provider unavailable"
		if info.Detail != "" {
			rep.skipped += ": " + info.Detail
		}
		return rep
	}
	if !info.Local && !set.Hosted {
		s.setRunning(false)
		rep.skipped = "hosted provider; automatic briefs stay on-box unless allowed"
		return rep
	}
	s.setRunning(true)
	roster, err := s.d.roster(ctx)
	if err != nil {
		s.fail(err)
		rep.errors++
		rep.skipped = "roster: " + err.Error()
		return rep
	}
	if roster.Shells == 0 {
		rep.skipped = "nobody attached"
		return rep
	}

	// Observe every eligible window; keep the ones that moved.
	type changed struct {
		entry wire.ObserveRosterEntry
		obs   wire.Observation
		key   string
	}
	var work []changed
	live := map[string]bool{}
	for _, e := range roster.Instances {
		if !e.Eligible {
			continue
		}
		live[e.InstanceID] = true
		obs, err := s.d.observe(ctx, e.InstanceID)
		if err != nil {
			rep.errors++
			s.fail(err)
			continue
		}
		rep.observed++
		if obs.Source == wire.ObserveSourceNone || obs.Content == "" {
			continue
		}
		key := obs.Source + ":" + obs.Revision
		if obs.Revision == "" {
			key = obs.Source + ":" + contentHash(obs.Content)
		}
		s.mu.Lock()
		prev := s.last[e.InstanceID]
		s.mu.Unlock()
		if prev != nil && prev.key == key {
			continue
		}
		work = append(work, changed{entry: e, obs: obs, key: key})
	}
	// Forget windows that are gone.
	s.mu.Lock()
	for id := range s.last {
		if !live[id] {
			delete(s.last, id)
		}
	}
	s.mu.Unlock()
	rep.changed = len(work)
	if len(work) == 0 {
		return rep
	}

	// Batch: at most BatchMax windows and batchBytes of content per
	// request, within the hour's budget.
	for start := 0; start < len(work); {
		end, size := start, 0
		for end < len(work) && end-start < set.BatchMax {
			if end > start && size+len(work[end].obs.Content) > batchBytes {
				break
			}
			size += len(work[end].obs.Content)
			end++
		}
		batch := work[start:end]
		start = end
		if !s.spend(set) {
			rep.skipped = "hourly budget spent"
			break
		}
		rep.requests++
		srcs := make([]activity.Source, len(batch))
		for i, w := range batch {
			srcs[i] = activity.Source{
				AppID: w.entry.App, Title: w.entry.Title, Kind: w.obs.Source,
				ContentType: w.obs.ContentType, Content: w.obs.Content, Truncated: w.obs.Truncated,
			}
		}
		briefs, meta, err := activity.GenerateBatch(ctx, s.d.gen, srcs)
		if err != nil {
			rep.errors++
			s.fail(err)
			// Remember what was sent so the same content is not retried
			// every tick; a change retries.
			s.mu.Lock()
			for _, w := range batch {
				s.last[w.entry.InstanceID] = &seen{key: w.key, at: s.d.now()}
			}
			s.mu.Unlock()
			if errors.Is(err, context.Canceled) {
				return rep
			}
			continue
		}
		for i, w := range batch {
			b := briefs[i]
			s.mu.Lock()
			prev := s.last[w.entry.InstanceID]
			now := s.d.now()
			if b == nil {
				// The model skipped it: keep the old brief, note the key.
				s.last[w.entry.InstanceID] = &seen{key: w.key, brief: prevBrief(prev), at: now}
				s.mu.Unlock()
				continue
			}
			if prev != nil && prev.brief != nil && sameBrief(*prev.brief, *b) {
				s.last[w.entry.InstanceID] = &seen{key: w.key, brief: b, at: now}
				s.mu.Unlock()
				rep.deduped++
				continue
			}
			s.last[w.entry.InstanceID] = &seen{key: w.key, brief: b, at: now}
			s.mu.Unlock()
			if err := s.d.note(briefNote(w.entry, w.obs, w.key, *b, meta)); err != nil {
				rep.errors++
				s.fail(err)
				continue
			}
			rep.briefs++
		}
	}
	return rep
}

func prevBrief(p *seen) *activity.Brief {
	if p == nil {
		return nil
	}
	return p.brief
}

// spend takes one request from the rolling hour; false when it is spent.
func (s *scheduler) spend(set Settings) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.d.now()
	if s.hourAt.IsZero() || now.Sub(s.hourAt) >= time.Hour {
		s.hourAt, s.hourUsed = now, 0
	}
	if s.hourUsed >= set.BudgetPerHour {
		return false
	}
	s.hourUsed++
	return true
}

func (s *scheduler) setRunning(on bool) {
	s.mu.Lock()
	s.stats.Running = on
	s.mu.Unlock()
}

func (s *scheduler) fail(err error) {
	s.mu.Lock()
	s.stats.LastError = err.Error()
	s.mu.Unlock()
	s.d.logf("wash-commander: %v", err)
}

// sameBrief says two briefs would read the same on a Timeline: goal,
// state, now and blockers. Lists of context or next steps reworded by
// the model are not news.
func sameBrief(a, b activity.Brief) bool {
	return strings.EqualFold(strings.TrimSpace(a.Goal), strings.TrimSpace(b.Goal)) &&
		a.State == b.State &&
		strings.TrimSpace(a.Now) == strings.TrimSpace(b.Now) &&
		joined(a.Blockers) == joined(b.Blockers)
}

func joined(xs []string) string {
	c := append([]string(nil), xs...)
	for i := range c {
		c[i] = strings.TrimSpace(c[i])
	}
	sort.Strings(c)
	return strings.Join(c, "\x00")
}

func contentHash(s string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%x", h.Sum64())
}

// briefNote is the journal entry for one brief (§5.2): the line reads
// "state · goal", the brief rides in ref, and the intent jumps to the
// window. Win is left zero — the commander does not own the window, and
// the router refuses a note that names one it does not.
func briefNote(e wire.ObserveRosterEntry, o wire.Observation, key string, b activity.Brief, meta activity.Meta) wire.EvtActivityNote {
	line := b.State + " · " + b.Goal
	if b.Now != "" {
		line += " — " + b.Now
	}
	title := e.Title
	if title == "" {
		title = e.App
	}
	return wire.EvtActivityNote{
		Kind: "brief", Title: title, Line: line,
		Ref: map[string]any{
			"brief": b, "source": o.Source, "revision": key,
			"provider": meta.Provider, "model": meta.Model,
			"app": e.App, "instance": e.InstanceID, "window": e.WindowID,
		},
		Intent: &wire.ActivityIntent{Kind: "focus", AppID: e.App, InstanceID: e.InstanceID, WindowID: e.WindowID},
	}
}
