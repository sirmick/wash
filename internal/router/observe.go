package router

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sirmick/wash/internal/observe"
	"github.com/sirmick/wash/pkg/wire"
)

// The observe verb (docs/COMMANDER.md §4): one look at one instance from
// what the router already holds. Resolution, in order — the app's own
// export when its manifest says so and it answers in time; the tail of a
// pty channel's scrollback ring; the persisted app_state blob; none. The
// answer names which, and every byte of content is redacted before it
// leaves the router. The router never calls a model; it only reads.

// observeExportGrace is how long an observation waits for an app's
// observe.reply before falling back to the automatic sources (§4.4).
const observeExportGrace = 250 * time.Millisecond

// permanentNone are the apps no manifest field can make observable: what
// they show is credentials, decisions and other apps' prompts.
var permanentNone = map[string]bool{
	"com.wash.priv":      true,
	"com.wash.settings":  true,
	"com.wash.inference": true,
	"com.wash.login":     true,
}

// observationMode resolves a manifest's observation field: auto for
// wash's own apps unless they say otherwise, none for everyone else, and
// none for the permanentNone set whatever they say.
func observationMode(m *Manifest) string {
	if m == nil || permanentNone[m.ID] {
		return ObservationNone
	}
	switch m.Observation {
	case ObservationAuto, ObservationExport, ObservationNone:
		return m.Observation
	}
	if strings.HasPrefix(m.ID, "com.wash.") {
		return ObservationAuto
	}
	return ObservationNone
}

// observe resolves one observation of inst. maxBytes bounds the content
// (0 or over the cap means the default tail); ctx bounds the wait for an
// export. Safe to call from any goroutine; it takes no router lock for
// longer than a map read.
func (r *Router) observe(ctx context.Context, inst *AppInstance, maxBytes int) wire.Observation {
	if maxBytes <= 0 || maxBytes > wire.ObserveMaxBytes {
		maxBytes = observe.DefaultTailBytes
	}
	o := wire.Observation{Source: wire.ObserveSourceNone, CapturedAt: time.Now().UnixMilli()}
	o.Window = &wire.ObservedWindow{App: inst.AppID, InstanceID: inst.InstanceID, WindowID: inst.WindowID}
	if w, ok := r.winSession.window(inst.WindowID); ok && inst.WindowID != 0 {
		o.Window.Title, o.Window.State, o.Window.Focused = w.Title, w.State, w.Focused
	}
	mode := observationMode(inst.Manifest)
	if mode == ObservationNone {
		return o
	}
	if mode == ObservationExport {
		if reply, ok := inst.requestObservation(ctx, maxBytes); ok && reply.Content != "" {
			o.Source = wire.ObserveSourceExport
			o.ContentType, o.Revision = reply.ContentType, reply.Revision
			if o.ContentType == "" {
				o.ContentType = "text/plain"
			}
			o.Content, o.Truncated = cutTail(reply.Content, maxBytes)
			o.Content = observe.Redact(o.Content)
			return o
		}
	}
	if text, revision, truncated, ok := r.ptyTail(inst, maxBytes); ok {
		o.Source, o.ContentType, o.Revision = wire.ObserveSourcePtyTail, "text/plain", revision
		o.Content, o.Truncated = observe.Redact(text), truncated
		return o
	}
	if state, version, ok := r.winSession.appStateFor(inst.InstanceID); ok && len(state) > 0 {
		o.Source, o.ContentType = wire.ObserveSourceAppState, "application/json"
		o.Revision = fmt.Sprintf("state:%d", version)
		o.Content, o.Truncated = cutTail(string(state), maxBytes)
		o.Content = observe.Redact(o.Content)
		return o
	}
	return o
}

// cutTail keeps the first max bytes of s on a rune boundary.
func cutTail(s string, max int) (string, bool) {
	if len(s) <= max {
		return s, false
	}
	cut := max
	for cut > 0 && cut < len(s) && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut], true
}

// ptyTail renders the tail of the busiest pty channel inst owns. A
// terminal with several tabs has several; the one written to last is the
// one the person is looking at, or the one doing the work.
func (r *Router) ptyTail(inst *AppInstance, maxBytes int) (text, revision string, truncated, ok bool) {
	var best *channelBinding
	var bestAt int64
	r.channelsMu.Lock()
	for _, b := range r.channels {
		if b.app != inst || b.peerConn != nil {
			continue
		}
		b.shellMu.Lock()
		if b.pty && b.buf != nil && b.seen > 0 && (best == nil || b.wroteAt > bestAt) {
			best, bestAt = b, b.wroteAt
		}
		b.shellMu.Unlock()
	}
	r.channelsMu.Unlock()
	if best == nil {
		return "", "", false, false
	}
	best.shellMu.Lock()
	raw := best.buf.Snapshot()
	wrapped := best.buf.Truncated()
	seen := best.seen
	best.shellMu.Unlock()
	text, truncated = observe.Terminal(raw, maxBytes)
	return text, fmt.Sprintf("pty:%d:%d", best.channelID, seen), truncated || wrapped, true
}

// requestObservation asks inst for its export and waits observeExportGrace
// (or ctx) for the reply. ok is false on no answer — the caller falls
// back, and a late reply is dropped.
func (inst *AppInstance) requestObservation(ctx context.Context, maxBytes int) (wire.EvtObserveReply, bool) {
	reqID := inst.router.nextObserveReq.Add(1)
	ch := make(chan wire.EvtObserveReply, 1)
	inst.observeMu.Lock()
	if inst.observeWaits == nil {
		inst.observeWaits = make(map[uint64]chan wire.EvtObserveReply)
	}
	inst.observeWaits[reqID] = ch
	inst.observeMu.Unlock()
	defer func() {
		inst.observeMu.Lock()
		delete(inst.observeWaits, reqID)
		inst.observeMu.Unlock()
	}()
	if err := inst.WriteEvt(wire.NewEvtObserveRequest(reqID, maxBytes)); err != nil {
		return wire.EvtObserveReply{}, false
	}
	timer := time.NewTimer(observeExportGrace)
	defer timer.Stop()
	select {
	case reply := <-ch:
		return reply, true
	case <-timer.C:
		inst.router.log("observe: export timeout app=%s instance=%s", inst.AppID, inst.InstanceID)
		return wire.EvtObserveReply{}, false
	case <-ctx.Done():
		return wire.EvtObserveReply{}, false
	}
}

// deliverObserveReply hands an app's export to the observation waiting
// for it. An unsolicited or late reply is dropped.
func (inst *AppInstance) deliverObserveReply(m wire.EvtObserveReply) {
	inst.observeMu.Lock()
	ch := inst.observeWaits[m.ReqID]
	inst.observeMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- m:
	default:
	}
}

// handleObserveGet is an app observing another instance (§4.1, attested).
// Gated by CapObserve; logged against the requester, never with content.
func (inst *AppInstance) handleObserveGet(m wire.EvtObserveGet) error {
	r := inst.router
	if !inst.Manifest.HasCapability(CapObserve) {
		r.log("observe: refused app=%s instance=%s: lacks CapObserve", inst.AppID, inst.InstanceID)
		return inst.WriteEvt(wire.NewEvtObserveGetErr(m.ReqID, wire.ErrCodeForbidden, "observe requires the observe capability"))
	}
	target := r.appByInstance(m.InstanceID)
	if target == nil {
		return inst.WriteEvt(wire.NewEvtObserveGetErr(m.ReqID, wire.ErrCodeNotFound, "no such instance"))
	}
	// Off the dispatch loop: an export waits on another app.
	go func() {
		o := r.observe(context.Background(), target, m.MaxBytes)
		r.log("observe: instance=%s app=%s source=%s bytes=%d by=%s", target.InstanceID, target.AppID, o.Source, len(o.Content), inst.AppID)
		_ = inst.WriteEvt(wire.NewEvtObserveResult(m.ReqID, o))
	}()
	return nil
}

// handleObserve is the shell's form of the verb.
func (s *ShellSession) handleObserve(m wire.ShellObserve) error {
	r := s.router
	target := r.appByInstance(m.InstanceID)
	if target == nil {
		return s.WriteCtrl(wire.NewShellObserveErr(m.ReqID, wire.ErrCodeNotFound, "no such instance"))
	}
	go func() {
		o := r.observe(context.Background(), target, m.MaxBytes)
		r.log("observe: instance=%s app=%s source=%s bytes=%d by=shell conn=%d", target.InstanceID, target.AppID, o.Source, len(o.Content), s.connID)
		_ = s.WriteCtrl(wire.NewShellObserveOK(m.ReqID, o))
	}()
	return nil
}
