package places

import (
	"fmt"
	"sort"
	"testing"

	"github.com/sirmick/wash/pkg/wire"
)

// The group protocol is several windows exchanging messages, so it is tested
// as exactly that: a tiny in-memory router that delivers app messages and
// spawn results in FIFO order, and lets a test queue clicks from several
// windows before any of them is delivered — which is how the races that
// single-message tests cannot see get exercised.

type fakeNet struct {
	t       *testing.T
	windows map[string]*fakeWin // instance id → window
	queue   []func()
	next    int
}

type fakeWin struct {
	net    *fakeNet
	app    string
	inst   string
	p      *Places
	raised int
	failed int
	// shown is the Path of every places.show this window received, in order.
	shown []string
}

func newNet(t *testing.T) *fakeNet {
	return &fakeNet{t: t, windows: map[string]*fakeWin{}}
}

// open makes a window of app as if the user launched it directly.
func (n *fakeNet) open(app string) *fakeWin {
	n.next++
	w := &fakeWin{net: n, app: app, inst: fmt.Sprintf("%s-%d", app[len("com.wash."):], n.next)}
	w.p = New(app, w.inst, nil)
	n.windows[w.inst] = w
	return w
}

// run delivers everything queued, and whatever that queues, until quiet. The
// cap turns a protocol that never converges into a failure, not a hang.
func (n *fakeNet) run() int {
	delivered := 0
	for len(n.queue) > 0 {
		f := n.queue[0]
		n.queue = n.queue[1:]
		f()
		delivered++
		if delivered > 500 {
			n.t.Fatalf("group protocol did not settle after %d deliveries", delivered)
		}
	}
	return delivered
}

// close removes a window; the router broadcasts instance.gone to everyone.
func (n *fakeNet) close(w *fakeWin) {
	delete(n.windows, w.inst)
	for _, o := range n.windows {
		o.p.OnInstanceGone(o, w.app, w.inst)
	}
}

func (n *fakeNet) only(app string) *fakeWin {
	n.t.Helper()
	var found *fakeWin
	for _, w := range n.windows {
		if w.app == app {
			if found != nil {
				n.t.Fatalf("more than one %s window", app)
			}
			found = w
		}
	}
	if found == nil {
		n.t.Fatalf("no %s window", app)
	}
	return found
}

func (n *fakeNet) count(app string) int {
	c := 0
	for _, w := range n.windows {
		if w.app == app {
			c++
		}
	}
	return c
}

func (w *fakeWin) SendAppMsg(any) error { return nil }

func (w *fakeWin) SendAppMsgTo(r wire.Recipient, data any) error {
	msg, ok := data.(Show)
	if !ok {
		w.net.t.Fatalf("unexpected payload %T", data)
	}
	from := wire.Sender{AppID: w.app, InstanceID: w.inst}
	w.net.queue = append(w.net.queue, func() {
		to := w.net.windows[r.InstanceID]
		if to == nil {
			return // closed in the meantime; the router would drop it too
		}
		to.p.Show(to, msg, from, func(_ Conn, req Show) { to.shown = append(to.shown, req.Path) })
	})
	return nil
}

func (w *fakeWin) SpawnRequestOpen(app, _ string) error {
	w.net.queue = append(w.net.queue, func() {
		nw := w.net.open(app)
		w.p.OnSpawnResult(w, app, nw.inst, nil)
	})
	return nil
}

func (w *fakeWin) Raise() error              { w.raised++; return nil }
func (w *fakeWin) Fail(string, error) error { w.failed++; return nil }

func (w *fakeWin) click(target string) {
	w.net.t.Helper()
	if err := w.p.Click(w, target, "/tmp", "", 0, 0); err != nil {
		w.net.t.Fatalf("%s click %s: %v", w.inst, target, err)
	}
}

// peers is the window's roster as sorted instance ids, for comparison.
func (w *fakeWin) peers() []string {
	var out []string
	for _, inst := range w.p.View().Members {
		out = append(out, inst)
	}
	sort.Strings(out)
	return out
}

// assertConverged checks the whole point of the protocol: every window in a
// group knows every other, they agree on the group id, and there is at most
// one window per app.
func assertConverged(t *testing.T, ws ...*fakeWin) {
	t.Helper()
	group := ws[0].p.View().Group
	if group == "" {
		t.Fatalf("%s is in no group", ws[0].inst)
	}
	apps := map[string]string{}
	for _, w := range ws {
		if g := w.p.View().Group; g != group {
			t.Errorf("%s is in group %q, want %q", w.inst, g, group)
		}
		if prev, dup := apps[w.app]; dup {
			t.Errorf("group has two %s windows: %s and %s", w.app, prev, w.inst)
		}
		apps[w.app] = w.inst
		var want []string
		for _, o := range ws {
			if o != w {
				want = append(want, o.inst)
			}
		}
		sort.Strings(want)
		if got := w.peers(); fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s knows %v, want %v", w.inst, got, want)
		}
	}
}

// The case that was broken: three members deep, the member that did NOT do
// the adding must still learn about the newcomer — or clicking from it opens
// a second window of the same app into the same group.
func TestAThirdMemberIsKnownToEveryone(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	agent.click(AppFiles)
	n.run()
	files := n.only(AppFiles)
	assertConverged(t, agent, term, files)

	// And the consequence the user would see: from the terminal, the Files
	// icon goes to the existing Files window rather than opening another.
	before := files.raised
	term.click(AppFiles)
	n.run()
	if got := n.count(AppFiles); got != 1 {
		t.Fatalf("clicking Files from the terminal opened another: %d Files windows", got)
	}
	if files.raised != before+1 {
		t.Errorf("the group's Files window was not brought forward")
	}
}

func TestAllFourAppsInOneGroup(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	// The next two members are added from DIFFERENT windows.
	term.click(AppFiles)
	n.run()
	files := n.only(AppFiles)
	files.click(AppEditor)
	n.run()
	assertConverged(t, agent, term, files, n.only(AppEditor))
}

// Two members add different apps before either hears about the other. Each
// broadcasts a roster missing the other's newcomer; convergence depends on
// every window that GROWS passing the roster on.
func TestConcurrentAddsFromDifferentMembersConverge(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)

	agent.click(AppFiles) // queued
	term.click(AppEditor) // queued before the first is delivered
	n.run()

	assertConverged(t, agent, term, n.only(AppFiles), n.only(AppEditor))
}

// Membership updates must not pull every window in the group to the front —
// only the one that was asked for.
func TestSyncDoesNotRaise(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	raisedBefore := term.raised
	agent.click(AppFiles)
	n.run()
	if term.raised != raisedBefore {
		t.Errorf("the terminal was raised by a membership update it did not ask for")
	}
	if agent.raised != 0 {
		t.Errorf("the clicking window raised itself")
	}
}

// A double click opens ONE window, and the later click's payload is the one
// it receives — the last click is what the user meant.
func TestDoubleClickOpensOneWindow(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	for _, path := range []string{"/first", "/second"} {
		if err := agent.p.Click(agent, AppEditor, "/tmp", path, 0, 0); err != nil {
			t.Fatal(err)
		}
	}
	n.run()
	editor := n.only(AppEditor)
	if fmt.Sprint(editor.shown) != "[/second]" {
		t.Fatalf("the new editor was shown %v, want only the later click", editor.shown)
	}
}

// Exclusivity has to hold on the RECEIVING side: the asker cannot know the
// target is taken.
func TestAMemberCannotBeTakenByAnotherGroup(t *testing.T) {
	n := newNet(t)
	a1 := n.open(AppAgent)
	a1.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	group := term.p.View().Group

	// A second agent whose idea of its group somehow names that terminal
	// (the only way a message could ever reach it) asks it forward.
	a2 := n.open(AppAgent)
	a2.p.mu.Lock()
	a2.p.group = "other"
	a2.p.members[AppTerminal] = term.inst
	a2.p.mu.Unlock()
	raised := term.raised
	a2.click(AppTerminal)
	n.run()

	if g := term.p.View().Group; g != group {
		t.Fatalf("the terminal was taken into another group: %q", g)
	}
	if term.p.View().Members[AppAgent] != a1.inst {
		t.Fatalf("the terminal's agent changed: %+v", term.p.View().Members)
	}
	if term.raised != raised {
		t.Error("a refused invitation still raised the window")
	}
}

func TestClosingAMemberFreesItsSlotEverywhere(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	agent.click(AppFiles)
	n.run()
	term, files := n.only(AppTerminal), n.only(AppFiles)

	n.close(files)
	for _, w := range []*fakeWin{agent, term} {
		if _, ok := w.p.View().Members[AppFiles]; ok {
			t.Errorf("%s still lists the closed Files window", w.inst)
		}
	}
	// The next click opens a fresh one, from any member.
	term.click(AppFiles)
	n.run()
	assertConverged(t, agent, term, n.only(AppFiles))
}

func TestTheLastMemberLeavingEndsTheGroup(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	n.close(n.only(AppTerminal))
	if v := agent.p.View(); v.Group != "" || len(v.Members) != 0 {
		t.Fatalf("a window alone still reports a group: %+v", v)
	}
}

// A spawn in flight keeps its group even if every other member closes
// meanwhile — the newcomer was promised that group, and an empty id would be
// rejected by the receiver.
func TestAPendingSpawnKeepsTheGroupAlive(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	agent.click(AppFiles) // queued, not delivered
	n.close(term)
	n.run()
	assertConverged(t, agent, n.only(AppFiles))
}

func TestAWindowAloneShowsNoGroup(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	// A click mints an id for the spawn, but until something joins, this
	// window belongs to nothing and must not be tinted.
	agent.click(AppFiles)
	if g := agent.p.View().Group; g != "" {
		t.Fatalf("a window with no peers reports group %q", g)
	}
}

func TestFailedSpawnFreesTheSlot(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppFiles)
	n.queue = nil // the router never starts it
	agent.p.OnSpawnResult(agent, AppFiles, "", fmt.Errorf("boom"))
	if agent.failed != 1 {
		t.Errorf("a failed spawn was not reported")
	}
	// Not stuck "in flight" forever: the next click tries again.
	agent.click(AppFiles)
	if len(n.queue) != 1 {
		t.Fatalf("the retry did not spawn")
	}
}

func TestAnUnrelatedSpawnIsIgnored(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	// The Agent opens editors for its own reasons too (editor.go); a spawn
	// result nobody here asked for must not be adopted into the group.
	agent.p.OnSpawnResult(agent, AppEditor, "e9", nil)
	if len(agent.p.View().Members) != 0 {
		t.Fatalf("adopted an unrelated spawn: %+v", agent.p.View().Members)
	}
}

func TestClickRefusesItselfAndStrangers(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	if err := agent.p.Click(agent, AppAgent, "/tmp", "", 0, 0); err == nil {
		t.Error("an app should not open itself")
	}
	if err := agent.p.Click(agent, "com.wash.music", "/tmp", "", 0, 0); err == nil {
		t.Error("a non-places app was accepted")
	}
}

func TestShowIgnoresSendersThatAreNotPlaces(t *testing.T) {
	n := newNet(t)
	term := n.open(AppTerminal)
	term.p.Show(term, Show{Kind: MsgKind, Group: "g1", Members: map[string]string{AppAgent: "a1"}},
		wire.Sender{AppID: "com.wash.music", InstanceID: "m1"}, nil)
	if v := term.p.View(); v.Group != "" {
		t.Fatalf("joined a group on a message from a non-places app: %+v", v)
	}
	if term.raised != 0 {
		t.Error("raised on a message from a non-places app")
	}
}

func TestShowFromRoundTrips(t *testing.T) {
	m := map[string]any{
		"kind": MsgSync, "group": "g1", "path": "/x", "line": float64(3), "col": float64(4),
		"members": map[string]any{AppAgent: "a1", AppFiles: 7},
	}
	got := ShowFrom(m)
	if got.Kind != MsgSync || got.Group != "g1" || got.Path != "/x" || got.Line != 3 || got.Col != 4 {
		t.Fatalf("fields: %+v", got)
	}
	// A mistyped member is dropped, not coerced.
	if len(got.Members) != 1 || got.Members[AppAgent] != "a1" {
		t.Fatalf("members: %+v", got.Members)
	}
	if ShowFrom(map[string]any{"kind": "anything"}).Kind != MsgKind {
		t.Error("an unknown kind should default to show")
	}
}

func TestKnown(t *testing.T) {
	for _, a := range Apps {
		if !Known(a) {
			t.Errorf("%s should be a places app", a)
		}
	}
	if Known("com.wash.music") {
		t.Error("music is not a places app")
	}
}

// The Agent's session travels with the group, so an Editor that joined
// through ANY member still learns which conversation it belongs to.
func TestAnEditorJoiningThroughTheTerminalLearnsTheSession(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.p.SetSession(agent, "k1", "Fix the viewport")
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	// The editor is opened from the terminal — it never hears from the
	// agent directly.
	term.click(AppEditor)
	n.run()
	editor := n.only(AppEditor)
	assertConverged(t, agent, term, editor)
	if v := editor.p.View(); v.Key != "k1" || v.Title != "Fix the viewport" {
		t.Fatalf("the editor did not learn the session: %+v", v)
	}
}

// A session attached AFTER the group formed (an autostarted agent binds its
// session a moment after its window joins) must still reach the members.
func TestASessionSetLaterReachesTheGroup(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.click(AppEditor)
	n.run()
	editor := n.only(AppEditor)
	if editor.p.View().Key != "" {
		t.Fatal("a session appeared from nowhere")
	}
	raised := editor.raised
	agent.p.SetSession(agent, "k2", "Draft")
	n.run()
	if v := editor.p.View(); v.Key != "k2" || v.Title != "Draft" {
		t.Fatalf("a later session did not reach the editor: %+v", v)
	}
	// A rename travels the same way — and neither pulls the editor forward.
	agent.p.SetSession(agent, "k2", "Draft, renamed")
	n.run()
	if editor.p.View().Title != "Draft, renamed" {
		t.Fatalf("a rename did not reach the editor: %+v", editor.p.View())
	}
	if editor.raised != raised {
		t.Error("a session update raised the editor")
	}
}

func TestTheAgentLeavingClearsTheSession(t *testing.T) {
	n := newNet(t)
	agent := n.open(AppAgent)
	agent.p.SetSession(agent, "k3", "Gone soon")
	agent.click(AppTerminal)
	n.run()
	term := n.only(AppTerminal)
	agent.click(AppFiles)
	n.run()
	n.close(agent)
	for _, w := range []*fakeWin{term, n.only(AppFiles)} {
		if v := w.p.View(); v.Key != "" {
			t.Errorf("%s still names the closed agent's session: %+v", w.inst, v)
		}
	}
}
