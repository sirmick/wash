// Package places is the "open the other apps, bound to this one" seam shared
// by the Agent, Files, Editor and Terminal windows (docs/PLACES.md).
//
// The model is two sentences: a window belongs to at most one GROUP, and a
// group holds at most one window of each app. Clicking another app's icon
// either raises the group's window of that app, or opens one and adds it to
// the group. A group is exclusive — nothing steals a member — and a window
// leaves only by closing.
//
// This generalises what the Agent already did privately for the Editor
// (apps/ai/be/editor.go): a remembered instance, a spawn-in-flight queue so a
// double click opens one window, adoption of the spawn result, and forgetting
// a peer that closed. The bugs those four pieces avoid are subtle enough that
// having them written once matters more than the lines saved.
//
// Raising follows the standing doctrine: an app may raise only its OWN window
// (pkg/sdk/outbound.go, apps/agentd/be/focus.go). Nothing here reaches across
// to raise a peer; it asks the peer, and the peer raises itself.
package places

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"sync"

	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

// MsgKind is the app-private message a member sends to bring another member
// forward, and by which a window is invited into a group.
const MsgKind = "places.show"

// MsgSync carries the same payload as MsgKind but only updates membership:
// no raise, no file opened. It is how the rest of a group learns that a
// member was added — see Places.broadcast.
const MsgSync = "places.sync"

// App ids of the four windows that take part.
const (
	AppAgent    = "com.wash.ai"
	AppFiles    = "com.wash.fm"
	AppEditor   = "com.wash.edit"
	AppTerminal = "com.wash.term"
)

// Apps is the roster, in the order the icon bar shows them.
var Apps = []string{AppAgent, AppFiles, AppEditor, AppTerminal}

// Known reports whether appID takes part in groups at all. A places.show
// from anything else is ignored rather than trusted.
func Known(appID string) bool {
	for _, a := range Apps {
		if a == appID {
			return true
		}
	}
	return false
}

// Show is the wire shape of MsgKind and MsgSync. It carries the WHOLE group
// rather than just the sender, so a window joining learns every member in one
// message. Membership converges without a registry because any window whose
// roster GROWS re-broadcasts it (Places.broadcast); a roster holds at most
// three peers, so each window can grow at most three times and the exchange
// terminates.
type Show struct {
	Kind string `json:"kind"`
	// Group identifies the group and, through a stable hash of it, picks the
	// tint every member shows (docs/PLACES.md §4.4). Derived, never assigned,
	// so two members cannot disagree about the colour.
	Group string `json:"group"`
	// Members is app id → instance id, including the sender.
	Members map[string]string `json:"members"`
	// Key and Title name the Agent session the group's agent is running.
	// Carried with the roster and propagated the same way, so an Editor that
	// joined through any member still learns which conversation it belongs
	// to — it titles itself after it, and can reopen it by key through
	// agentd even after the Agent window is gone.
	Key   string `json:"key,omitempty"`
	Title string `json:"title,omitempty"`
	// Path is what the sender was looking at, for a member that wants to
	// follow (the Editor opening a file). Empty means "just come forward".
	Path string `json:"path,omitempty"`
	Line int    `json:"line,omitempty"`
	Col  int    `json:"col,omitempty"`
}

// ShowFrom rebuilds a Show from a decoded app message, for apps that
// dispatch raw maps rather than through a Bus. Unknown and mistyped fields
// are dropped rather than guessed — the sender is attested, but the payload
// is still just JSON.
func ShowFrom(m map[string]any) Show {
	out := Show{Kind: MsgKind, Members: map[string]string{}}
	if k, _ := m["kind"].(string); k == MsgSync {
		out.Kind = MsgSync
	}
	out.Group, _ = m["group"].(string)
	out.Key, _ = m["key"].(string)
	out.Title, _ = m["title"].(string)
	out.Path, _ = m["path"].(string)
	if n, ok := m["line"].(float64); ok {
		out.Line = int(n)
	}
	if n, ok := m["col"].(float64); ok {
		out.Col = int(n)
	}
	members, _ := m["members"].(map[string]any)
	for k, v := range members {
		if s, ok := v.(string); ok {
			out.Members[k] = s
		}
	}
	return out
}

// Conn is the slice of *sdk.Conn this package uses. *sdk.Conn satisfies it,
// so apps pass their connection unchanged; it exists so the group protocol
// — which is several windows exchanging messages — can be tested as exactly
// that, without a router.
type Conn interface {
	SendAppMsg(data any) error
	SendAppMsgTo(recipient wire.Recipient, data any) error
	SpawnRequestOpen(appID, path string) error
	Raise() error
	Fail(title string, err error) error
}

var _ Conn = (*sdk.Conn)(nil)

// View is what the FE needs to draw the icon bar: which group this window is
// in (for the tint) and which apps are reachable (for the fill).
type View struct {
	Group   string            `json:"group"`
	Members map[string]string `json:"members"`
	// Key and Title: the group's Agent session, when there is one.
	Key   string `json:"key,omitempty"`
	Title string `json:"title,omitempty"`
}

// Places is one window's membership. Safe for concurrent use: the bus, the
// spawn callback and the FE all touch it.
type Places struct {
	appID string
	// self is this window's own instance id, so the group it hands out names
	// this window too — without it a joiner would learn every member except
	// the one that invited it.
	self string
	// onChange is called after every membership change so the app can push a
	// fresh View to its FE. It is handed the View rather than reading it
	// back, so an app can declare the callback beside its package-level
	// Places without the two referring to each other.
	onChange func(Conn, View)

	mu      sync.Mutex
	group   string
	members map[string]string
	// key and title: the group's Agent session (see Show.Key).
	key, title string
	// pending is the app a spawn is in flight for, and what to send it once
	// it lands. Keyed by app id: two different apps may be starting at once,
	// but a second click on the SAME app must not start a second window.
	pending map[string]Show
}

// New makes the seam for one window. appID and instanceID identify this
// window; onChange is invoked whenever the group changes so the caller can
// refresh its FE.
func New(appID, instanceID string, onChange func(Conn, View)) *Places {
	return &Places{
		appID:    appID,
		self:     instanceID,
		onChange: onChange,
		members:  map[string]string{},
		pending:  map[string]Show{},
	}
}

// View is the current membership, for the FE. A window with no peers reports
// no group even while it holds an id for a spawn in flight: the tint means
// "these windows belong together", and one window alone belongs to nothing.
func (p *Places) View() View {
	p.mu.Lock()
	defer p.mu.Unlock()
	m := make(map[string]string, len(p.members))
	for k, v := range p.members {
		m[k] = v
	}
	if len(m) == 0 {
		return View{Members: m}
	}
	return View{Group: p.group, Members: m, Key: p.key, Title: p.title}
}

// Register installs the places.show handler on a bus. The sender is
// router-attested (HandleFromVoid), so a window cannot talk its way in.
func (p *Places) Register(b *sdk.Bus, onShow func(c Conn, req Show)) {
	sdk.HandleFromVoid(b, MsgKind, func(c *sdk.Conn, _ string, req Show, from wire.Sender) error {
		req.Kind = MsgKind
		p.Show(c, req, from, onShow)
		return nil
	})
	sdk.HandleFromVoid(b, MsgSync, func(c *sdk.Conn, _ string, req Show, from wire.Sender) error {
		req.Kind = MsgSync
		p.Show(c, req, from, onShow)
		return nil
	})
}

// Show is Register's body, exposed for apps that dispatch app messages
// themselves rather than through a Bus (the Agent window does). The caller
// must pass the router-attested sender it was given, never one off the wire.
//
// req.Kind decides the effect: MsgKind raises this window and runs onShow;
// MsgSync only updates membership.
func (p *Places) Show(c Conn, req Show, from wire.Sender, onShow func(c Conn, req Show)) {
	if !Known(from.AppID) || from.InstanceID == "" || req.Group == "" {
		return
	}
	grew, err := p.join(req, from)
	if err != nil {
		// Exclusivity is not an error the user asked for; the clicking
		// window simply gets its own. Logged, not surfaced.
		log.Printf("places: declined %s from %s: %v", req.Kind, from.AppID, err)
		return
	}
	if grew {
		p.changed(c)
		p.broadcast(c, "")
	}
	if req.Kind == MsgSync {
		return
	}
	// Raise OURSELVES. The sender never raises us.
	if err := c.Raise(); err != nil {
		log.Printf("places: raise: %v", err)
	}
	if onShow != nil {
		onShow(c, req)
	}
}

// join adopts the sender's group, or refuses when this window already belongs
// to a different one. grew reports whether it learned a member (or a new
// instance for an app) it did not know, which is what obliges it to pass the
// roster on.
func (p *Places) join(req Show, from wire.Sender) (grew bool, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.group != "" && p.group != req.Group {
		return false, fmt.Errorf("already in group %s", p.group)
	}
	p.group = req.Group
	adopt := func(app, inst string) {
		if !Known(app) || app == p.appID || inst == "" {
			return
		}
		if p.members[app] != inst {
			p.members[app] = inst
			grew = true
		}
	}
	for app, inst := range req.Members {
		adopt(app, inst)
	}
	adopt(from.AppID, from.InstanceID)
	// The session travels like a member: learning it (or a rename) obliges
	// passing it on. An empty key says nothing — most windows never knew it.
	if req.Key != "" && (req.Key != p.key || req.Title != p.title) {
		p.key, p.title = req.Key, req.Title
		grew = true
	}
	return grew, nil
}

// action is what a click resolves to, decided under the lock and performed
// outside it. Split out so the decision — which is where the double-click and
// exclusivity rules live — is testable without a live Conn.
type action struct {
	// sendTo is the instance to bring forward; empty when nothing is there.
	sendTo string
	// spawn is the app to open; empty when a window already exists or a
	// spawn for it is already in flight.
	spawn string
	msg   Show
}

// plan decides what clicking target should do, and records a spawn as pending
// if it starts one.
func (p *Places) plan(target, path string, line, col int) (action, error) {
	if !Known(target) || target == p.appID {
		return action{}, errors.New("not a places app")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.group == "" {
		p.group = newGroupID()
	}
	msg := p.showLocked(path, line, col)
	if inst := p.members[target]; inst != "" {
		return action{sendTo: inst, msg: msg}, nil
	}
	if _, busy := p.pending[target]; busy {
		// A spawn is already on its way. Replace what it will be sent — the
		// last click is the one the user meant — but do not start a second
		// window.
		p.pending[target] = msg
		return action{}, nil
	}
	p.pending[target] = msg
	return action{spawn: target, msg: msg}, nil
}

// Click is the icon for target being clicked. Either the group's window of
// that app is asked to come forward, or one is opened at cwd and adopted.
//
// cwd comes from the FE and MUST already be confined by the caller: every
// app's idea of "the folder I am looking at" lives in its frontend (Files
// navigates, the Terminal tracks a cwd per tab), so the BE is told rather
// than asking, exactly as the existing open-with verbs do.
//
// path/line/col are optional and travel to the target (the Editor uses them
// to open a file); an empty path means "just come forward".
func (p *Places) Click(c Conn, target, cwd, path string, line, col int) error {
	a, err := p.plan(target, path, line, col)
	if err != nil {
		return err
	}
	// An audit line per click, like every other cross-app verb: which window
	// asked for what, and whether it raised one or opened one where.
	switch {
	case a.sendTo != "":
		log.Printf("places: %s show %s instance=%s group=%s", p.appID, target, a.sendTo, a.msg.Group)
		return c.SendAppMsgTo(wire.Recipient{InstanceID: a.sendTo}, a.msg)
	case a.spawn != "":
		log.Printf("places: %s open %s dir=%q group=%s", p.appID, a.spawn, cwd, a.msg.Group)
		if err := c.SpawnRequestOpen(a.spawn, cwd); err != nil {
			p.mu.Lock()
			delete(p.pending, a.spawn)
			p.mu.Unlock()
			return err
		}
	}
	return nil
}

// showLocked builds the message describing this group as it now stands,
// including this window. Caller holds p.mu.
func (p *Places) showLocked(path string, line, col int) Show {
	m := make(map[string]string, len(p.members)+1)
	for k, v := range p.members {
		m[k] = v
	}
	if p.self != "" {
		m[p.appID] = p.self
	}
	return Show{Kind: MsgKind, Group: p.group, Members: m, Key: p.key, Title: p.title, Path: path, Line: line, Col: col}
}

// OnSpawnResult adopts a window opened by Click. The app forwards its
// AppDef.OnSpawnResult here; a spawn this seam did not ask for is ignored.
//
// The new window is sent the roster as it stands NOW, not as it stood at
// click time — another member may have joined while this one was starting —
// and the rest of the group is told about it (broadcast), because otherwise
// they would never learn it exists and would open a second one.
func (p *Places) OnSpawnResult(c Conn, appID, instanceID string, err error) {
	p.mu.Lock()
	clicked, waiting := p.pending[appID]
	if !waiting {
		p.mu.Unlock()
		return
	}
	delete(p.pending, appID)
	if err != nil {
		p.mu.Unlock()
		log.Printf("places: spawn %s: %v", appID, err)
		_ = c.Fail("Could not open "+appID, err)
		return
	}
	p.members[appID] = instanceID
	msg := p.showLocked(clicked.Path, clicked.Line, clicked.Col)
	p.mu.Unlock()
	p.changed(c)
	if err := c.SendAppMsgTo(wire.Recipient{InstanceID: instanceID}, msg); err != nil {
		log.Printf("places: %s to %s: %v", MsgKind, instanceID, err)
	}
	p.broadcast(c, instanceID)
}

// broadcast tells every member except skip (and this window) the roster as
// it now stands. Called whenever this window's roster grows — never when it
// does not, which is what makes the exchange terminate.
func (p *Places) broadcast(c Conn, skip string) {
	p.mu.Lock()
	msg := p.showLocked("", 0, 0)
	msg.Kind = MsgSync
	var targets []string
	for _, inst := range p.members {
		if inst != skip {
			targets = append(targets, inst)
		}
	}
	p.mu.Unlock()
	for _, inst := range targets {
		if err := c.SendAppMsgTo(wire.Recipient{InstanceID: inst}, msg); err != nil {
			log.Printf("places: %s to %s: %v", MsgSync, inst, err)
		}
	}
}

// OnInstanceGone drops a member that closed, freeing its slot for the next
// click. The app forwards its AppDef.OnInstanceGone here.
func (p *Places) OnInstanceGone(c Conn, _ string, instanceID string) {
	p.mu.Lock()
	var dropped bool
	for app, inst := range p.members {
		if inst == instanceID {
			delete(p.members, app)
			dropped = true
			// The session belonged to the agent member; with it gone the
			// group has no conversation. (The Editor keeps its own copy of
			// the key to reopen it — that is the Editor's business.)
			if app == AppAgent {
				p.key, p.title = "", ""
			}
		}
	}
	// The last peer leaving ends the group, so the next click mints a fresh
	// one rather than reviving a colour the user watched disappear — unless
	// a spawn is still in flight, which was promised THIS group and would
	// otherwise arrive carrying an empty id that every receiver rejects.
	if dropped && len(p.members) == 0 && len(p.pending) == 0 {
		p.group = ""
	}
	p.mu.Unlock()
	if dropped {
		p.changed(c)
	}
}

func (p *Places) changed(c Conn) {
	if p.onChange != nil {
		p.onChange(c, p.View())
	}
}

// SetSession records the Agent session this window runs (the Agent calls
// it; no other app has one) and, when that changes while there are peers,
// tells them — an Editor already in the group must learn a session the
// Agent only attached after joining, or a rename.
func (p *Places) SetSession(c Conn, key, title string) {
	p.mu.Lock()
	changed := key != p.key || title != p.title
	p.key, p.title = key, title
	peers := len(p.members)
	p.mu.Unlock()
	if changed && peers > 0 {
		p.changed(c)
		p.broadcast(c, "")
	}
}

// SetSelf records this window's instance id, which is only known once the
// app is ready. A Places is built at package scope so callbacks always have
// something to talk to; this fills in the half that has to wait.
func (p *Places) SetSelf(instanceID string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.self = instanceID
}

// newGroupID is random rather than derived from an instance id: the id seeds
// the group's colour, and instance ids are allocated in order, so deriving
// from one would make consecutive groups pick neighbouring tints.
func newGroupID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "g"
	}
	return hex.EncodeToString(b[:])
}
