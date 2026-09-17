package router

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/sirmick/wash/internal/wiretest"
	"github.com/sirmick/wash/pkg/wire"
)

// observeInstance is an app instance registered with r, with a piped
// transport whose far end the test reads (an export round trip) or
// drains (everything else). The router side is pumped the way HandleApp
// would: every frame the far end writes is dispatched.
func observeInstance(t *testing.T, r *Router, id string, m *Manifest) (*AppInstance, wire.FrameTransport) {
	t.Helper()
	pair := wiretest.NewPipePair()
	inst := &AppInstance{AppID: m.ID, InstanceID: id, WindowID: 4, Manifest: m, router: r, Transport: pair.EndA()}
	r.mu.Lock()
	r.apps[id] = inst
	r.mu.Unlock()
	go func() {
		for {
			f, err := pair.EndA().ReadFrame()
			if err != nil {
				return
			}
			_ = inst.dispatch(f)
		}
	}()
	t.Cleanup(pair.Close)
	return inst, pair.EndB()
}

func drainFrames(e wire.FrameTransport) {
	go func() {
		for {
			if _, err := e.ReadFrame(); err != nil {
				return
			}
		}
	}()
}

// The manifest field decides, with wash's own apps observable by default,
// anyone else's not, and a few never.
func TestObservationModeResolution(t *testing.T) {
	cases := map[*Manifest]string{
		&Manifest{ID: "com.wash.term"}:                                   ObservationAuto,
		&Manifest{ID: "com.wash.term", Observation: ObservationNone}:     ObservationNone,
		&Manifest{ID: "com.wash.edit", Observation: ObservationExport}:   ObservationExport,
		&Manifest{ID: "org.example.thing"}:                               ObservationNone,
		&Manifest{ID: "org.example.thing", Observation: ObservationAuto}: ObservationAuto,
		&Manifest{ID: "com.wash.priv", Observation: ObservationAuto}:     ObservationNone,
		&Manifest{ID: "com.wash.inference"}:                              ObservationNone,
		nil:                                                              ObservationNone,
	}
	for m, want := range cases {
		if got := observationMode(m); got != want {
			t.Errorf("observationMode(%+v) = %q, want %q", m, got, want)
		}
	}
	if err := wire.ValidateManifest(&wire.Manifest{ID: "com.wash.x", Name: "x", Version: "1", ProtocolVersion: wire.ProtocolVersion,
		Element: "wash-app-x", Surface: wire.SurfaceWindow, Icon: "data:x", Instancing: wire.InstancingMulti, Observation: "sometimes"}); err == nil || !strings.Contains(err.Error(), "observation") {
		t.Fatalf("an unknown observation value validated: %v", err)
	}
}

// A terminal's observation is the tail of its pty ring as text: escape
// sequences gone, a pasted token gone, and a revision that moves with
// the bytes. Only pty channels count — a file upload on a generic channel
// is never read.
func TestObservePtyTailIsStrippedRedactedAndRevisioned(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	m := aboutManifest()
	m.ID = "com.wash.term"
	inst, far := observeInstance(t, r, "i-term", m)
	drainFrames(far)

	// A generic (non-pty) channel with content: invisible to observe.
	r.registerChannel(&channelBinding{channelID: 77, app: inst, kind: wire.ChannelKindGeneric, buf: newRingBuffer(ChannelScrollbackBytes), credit: NewChannelCredit(0)})
	if err := inst.dispatch(wire.Frame{Channel: 77, Payload: []byte("PNG garbage")}); err != nil {
		t.Fatal(err)
	}
	o := r.observe(context.Background(), inst, 0)
	if o.Source != wire.ObserveSourceNone {
		t.Fatalf("a generic channel was observed: %+v", o)
	}

	r.registerChannel(&channelBinding{channelID: 78, app: inst, kind: wire.ChannelKindGeneric, pty: true, buf: newRingBuffer(ChannelScrollbackBytes), credit: NewChannelCredit(0)})
	raw := "\x1b[1;32mmick@buzz\x1b[0m$ export TOKEN=abc123secret\r\n\x1b[?25lmake test\r\nok\r\n"
	if err := inst.dispatch(wire.Frame{Channel: 78, Payload: []byte(raw)}); err != nil {
		t.Fatal(err)
	}
	o = r.observe(context.Background(), inst, 0)
	if o.Source != wire.ObserveSourcePtyTail || o.ContentType != "text/plain" {
		t.Fatalf("observation=%+v", o)
	}
	if o.Content != "mick@buzz$ export TOKEN=[redacted]\nmake test\nok" {
		t.Fatalf("content=%q", o.Content)
	}
	if o.Window == nil || o.Window.App != "com.wash.term" || o.Window.InstanceID != "i-term" {
		t.Fatalf("window=%+v", o.Window)
	}
	rev := o.Revision
	if !strings.HasPrefix(rev, "pty:78:") {
		t.Fatalf("revision=%q", rev)
	}
	// Same bytes, same revision; more bytes, a new one.
	if again := r.observe(context.Background(), inst, 0); again.Revision != rev {
		t.Fatalf("revision moved without output: %q → %q", rev, again.Revision)
	}
	_ = inst.dispatch(wire.Frame{Channel: 78, Payload: []byte("$ \r\n")})
	if again := r.observe(context.Background(), inst, 0); again.Revision == rev {
		t.Fatal("revision did not move with output")
	}
}

// With no pty, the saved app_state blob answers, versioned per set; with
// nothing saved either, none — and a none manifest answers none even
// with a blob.
func TestObserveFallsBackToStateBlobThenNone(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	inst, far := observeInstance(t, r, "i-edit", aboutManifest())
	drainFrames(far)

	// Nothing held, but the app is auto: the shell is told to keep looking.
	if o := r.observe(context.Background(), inst, 0); o.Source != wire.ObserveSourceNone || o.CapturedAt == 0 || !o.Eligible {
		t.Fatalf("empty instance observed as %+v", o)
	}
	r.winSession.setAppState("i-edit", json.RawMessage(`{"tabs":[{"path":"/tmp/a.go","password":"hunter2"}]}`))
	o := r.observe(context.Background(), inst, 0)
	if o.Source != wire.ObserveSourceAppState || o.ContentType != "application/json" || o.Revision != "state:1" {
		t.Fatalf("observation=%+v", o)
	}
	if strings.Contains(o.Content, "hunter2") || !strings.Contains(o.Content, "/tmp/a.go") {
		t.Fatalf("content=%q", o.Content)
	}
	r.winSession.setAppState("i-edit", json.RawMessage(`{"tabs":[]}`))
	if o := r.observe(context.Background(), inst, 0); o.Revision != "state:2" {
		t.Fatalf("revision=%q after a second set", o.Revision)
	}
	// A cap smaller than the blob cuts and says so.
	if o := r.observe(context.Background(), inst, 4); len(o.Content) != 4 || !o.Truncated {
		t.Fatalf("cap ignored: %+v", o)
	}

	inst.Manifest.Observation = ObservationNone
	if o := r.observe(context.Background(), inst, 0); o.Source != wire.ObserveSourceNone || o.Content != "" || o.Eligible {
		t.Fatalf("a none app was observed: %+v", o)
	}
}

// An export app is asked and its answer is the observation; an app that
// does not answer in time, or answers empty, falls back to what the
// router holds.
func TestObserveExportRoundTripAndFallback(t *testing.T) {
	r := NewRouter(Config{}, NewRegistry(), func(string, ...any) {})
	m := aboutManifest()
	m.Observation = ObservationExport
	inst, far := observeInstance(t, r, "i-x", m)
	r.winSession.setAppState("i-x", json.RawMessage(`{"fallback":true}`))

	// The app side: answer the first request, ignore the second, answer
	// the third empty.
	answers := make(chan int, 3)
	answers <- 1
	answers <- 0
	answers <- 2
	go func() {
		for {
			f, err := far.ReadFrame()
			if err != nil {
				return
			}
			m, _ := wire.DecodeEvt(f.Payload)
			req, ok := m.(wire.EvtObserveRequest)
			if !ok {
				continue
			}
			switch <-answers {
			case 1:
				writeEvt(t, far, wire.NewEvtObserveReply(req.ReqID, "text/plain", "cwd=/home/mick token=zzz", "r7"))
			case 2:
				writeEvt(t, far, wire.NewEvtObserveReply(req.ReqID, "", "", ""))
			}
		}
	}()

	o := r.observe(context.Background(), inst, 0)
	if o.Source != wire.ObserveSourceExport || o.Revision != "r7" || o.Content != "cwd=/home/mick token=[redacted]" {
		t.Fatalf("export observation=%+v", o)
	}
	start := time.Now()
	if o := r.observe(context.Background(), inst, 0); o.Source != wire.ObserveSourceAppState {
		t.Fatalf("no answer did not fall back: %+v", o)
	} else if el := time.Since(start); el < observeExportGrace || el > 2*time.Second {
		t.Fatalf("fallback after %v, want ~%v", el, observeExportGrace)
	}
	if o := r.observe(context.Background(), inst, 0); o.Source != wire.ObserveSourceAppState {
		t.Fatalf("an empty answer did not fall back: %+v", o)
	}
	// A late reply for a request nobody waits on is dropped, not a panic.
	inst.deliverObserveReply(wire.NewEvtObserveReply(999, "", "late", ""))
}

// The shell's verb answers for any instance of this router's and says
// not_found otherwise; the log names source and size, never content.
func TestShellObserveVerb(t *testing.T) {
	r, lc, shell, cleanup := journalRouter(t, Config{NoActivity: true})
	defer cleanup()
	inst, far := observeInstance(t, r, "i-term", aboutManifest())
	drainFrames(far)
	r.registerChannel(&channelBinding{channelID: 79, app: inst, kind: wire.ChannelKindGeneric, pty: true, buf: newRingBuffer(ChannelScrollbackBytes), credit: NewChannelCredit(0)})
	_ = inst.dispatch(wire.Frame{Channel: 79, Payload: []byte("$ git status\r\nclean\r\n")})

	writeCtrl(t, shell, wire.NewShellObserve(5, "i-term", 0))
	ok := readCtrlOfType[wire.ShellObserveOK](t, shell)
	if ok.ReqID != 5 || ok.Observation.Source != wire.ObserveSourcePtyTail || ok.Observation.Content != "$ git status\nclean" {
		t.Fatalf("observe answered %+v", ok)
	}
	if !lc.contains("observe: instance=i-term app=com.wash.about source=pty-tail bytes=18 by=shell") || lc.contains("git status") {
		t.Fatalf("log did not name the observation (or quoted it): %v", lc.lines)
	}
	writeCtrl(t, shell, wire.NewShellObserve(6, "i-nope", 0))
	if e := readCtrlOfType[wire.ShellObserveErr](t, shell); e.ReqID != 6 || e.Code != wire.ErrCodeNotFound {
		t.Fatalf("err=%+v", e)
	}
}

// An app observing another instance needs the capability; with it the
// answer rides back on its own event channel.
func TestAppObserveGetIsGated(t *testing.T) {
	r, lc, _, cleanup := journalRouter(t, Config{NoActivity: true})
	defer cleanup()
	target, tfar := observeInstance(t, r, "i-target", aboutManifest())
	drainFrames(tfar)
	r.winSession.setAppState("i-target", json.RawMessage(`{"doc":"hello"}`))

	bare, bfar := observeInstance(t, r, "i-bare", aboutManifest())
	_ = bare.handleObserveGet(wire.NewEvtObserveGet(1, "i-target", 0))
	if e, ok := readEvt(t, bfar).(wire.EvtObserveGetErr); !ok || e.Code != wire.ErrCodeForbidden {
		t.Fatalf("without the capability: %+v", e)
	}
	if !lc.contains("lacks CapObserve") {
		t.Fatal("refusal not logged")
	}

	cm := aboutManifest()
	cm.ID = "com.wash.commander"
	cm.Capabilities = []string{CapObserve}
	asker, afar := observeInstance(t, r, "i-cmd", cm)
	_ = asker.handleObserveGet(wire.NewEvtObserveGet(2, "i-target", 0))
	res, ok := readEvt(t, afar).(wire.EvtObserveResult)
	if !ok || res.ReqID != 2 || res.Observation.Source != wire.ObserveSourceAppState || !strings.Contains(res.Observation.Content, "hello") {
		t.Fatalf("result=%+v ok=%v", res, ok)
	}
	if !lc.contains("by=com.wash.commander") {
		t.Fatal("the requester was not logged")
	}
	_ = target
}

// The roster is every windowed instance with whether observe would look,
// plus how many shells are attached; it needs the capability too.
func TestObserveRosterListsWindowedInstances(t *testing.T) {
	r, lc, _, cleanup := journalRouter(t, Config{NoActivity: true})
	lcOf := func(*Router) *logCapture { return lc }
	defer cleanup()
	term := aboutManifest()
	term.ID = "com.wash.term"
	priv := aboutManifest()
	priv.ID = "com.wash.priv"
	a, af := observeInstance(t, r, "i-a", term)
	drainFrames(af)
	b, bf := observeInstance(t, r, "i-b", priv)
	drainFrames(bf)
	b.WindowID = 9
	r.winSession.createWindow(a.WindowID, "i-a", "wash-app-term", "", "", "~/wash", 0, 0, 0, 0, 0, 0, false, false)
	r.winSession.createWindow(9, "i-b", "wash-app-priv", "", "", "priv", 0, 0, 0, 0, 0, 0, false, false)
	bg := aboutManifest()
	bg.ID = "com.wash.commander"
	bg.Capabilities = []string{CapObserve}
	cmd, cf := observeInstance(t, r, "i-cmd", bg)
	cmd.WindowID = 0

	_ = cmd.handleObserveRoster(wire.NewEvtObserveRoster(3))
	res, ok := readEvt(t, cf).(wire.EvtObserveRosterResult)
	if !ok || res.ReqID != 3 || res.Shells != 1 || len(res.Instances) != 2 {
		t.Fatalf("roster=%+v ok=%v", res, ok)
	}
	if res.Instances[0].InstanceID != "i-a" || !res.Instances[0].Eligible || res.Instances[0].Title != "~/wash" {
		t.Fatalf("term entry=%+v", res.Instances[0])
	}
	if res.Instances[1].InstanceID != "i-b" || res.Instances[1].Eligible {
		t.Fatalf("priv entry=%+v", res.Instances[1])
	}

	// Without the capability: refused and logged.
	_ = a.handleObserveRoster(wire.NewEvtObserveRoster(4))
	if !lcOf(r).contains("roster refused app=com.wash.term") {
		t.Fatal("roster without the capability was not refused")
	}
}
