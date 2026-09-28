// Package ai provides two deliberately separate surfaces over agentd:
// com.wash.agents is the singleton roster/history/launcher manager, while
// each com.wash.ai process controls exactly one hosted session.
//
// It is a thin host. agentd owns the session, the transcript, the roster
// and the approval queue; this app owns a window. The FE speaks agentd's
// protocol (internal/agentproto, docs/AGENT_PROTOCOL.md) and this backend
// relays it: a registered request from the FE goes to agentd as it is, and
// a registered push from agentd comes back as it is, in its class, unless
// it is keyed to a session this window is not showing. What the backend
// does itself is the window's business: which session it shows (bind,
// restore), its title and taskbar attention, closing it (detach or stop),
// saving a transcript, opening an app in the session's folder, and its own
// editor (editor.go).
//
// And ONE message this app accepts from an app that is not agentd:
//
//	any app → ai   {kind: "agent_draft", text}
//
// which inserts `text` into this window's composer. It is a DRAFT, never
// a prompt: it lands at the caret and waits, because what a person does
// with a selection someone sent them — frame it with a question, trim it,
// think better of it — is the whole reason it goes to a composer rather
// than to the agent. wash-edit's "send selection to agent" uses exactly
// this shape; keep the kind stable, other apps will grow the same verb.
//
// Only the manager subscribes to agentd's global roster. A controller gets a
// keyed row/ask view plus its keyed transcript, avoiding N copies of the
// complete backend data structure on every active window.
package ai

import (
	"context"
	"embed"
	"flag"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentd "github.com/sirmick/wash/apps/agentd/be"
	"github.com/sirmick/wash/internal/agentpolicy"
	"github.com/sirmick/wash/internal/agentproto"
	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/pkg/apps/registry"
	"github.com/sirmick/wash/pkg/sdk"
	"github.com/sirmick/wash/pkg/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// aiIcon — Lucide sprite symbol; added to web/shell/build-icons.mjs.
const aiIcon = "bot"

const agentdAppID = agentd.AppID

// draftKind is the app message any app may send this one to put text in
// its composer. Stable on purpose: wash-edit's "send selection to agent"
// is written against this exact shape, and so will the next app's be.
const draftKind = "agent_draft"

// aiDebug traces the roster subscription, which is what drives the status
// line and the working spinner. Off unless WASH_AGENT_DEBUG is set.
var aiDebug = os.Getenv("WASH_AGENT_DEBUG") != ""

// maxTranscriptBytes bounds a saved transcript. Generous — a long
// session with images is large — but finite, because the FE hands us the
// bytes and a bound is cheaper than trusting it.
const maxTranscriptBytes = 32 << 20

var aiFS *wfs.FS

var def *sdk.AppDef
var managerDef *sdk.AppDef
var managerMode bool

func init() {
	sub, err := fs.Sub(assetsFS, "assets")
	if err != nil {
		panic("wash-ai: assets: " + err.Error())
	}
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              "com.wash.ai",
			Name:            "Agent",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Element:         "wash-app-ai",
			Surface:         sdk.SurfaceWindow,
			Icon:            aiIcon,
			Accent:          "violet",
			Instancing:      sdk.InstancingMulti,
			Hidden:          true,
			Capabilities:    []string{sdk.CapSpawn},
			// 600, not 720: the composer grew a row (Attach…) and the
			// status bar grew root chips, and a window as tall as a
			// 720-line screen left both under the 40px taskbar. Sized so
			// the whole window, status bar included, fits above it on the
			// smallest screen this desktop targets.
			Window: &sdk.WindowHints{DefaultWidth: 620, DefaultHeight: 600},
		},
		Assets:           sub,
		OnReady:          onReady,
		OnAppMsg:         onAppMsg,
		OnAppMsgFrom:     onAppMsgFrom,
		OnCloseRequested: onCloseRequested,
		OnSpawnResult:    onSpawnResult,
		OnInstanceGone:   onInstanceGone,
	}
	registry.Register(&registry.App{
		Name:     "wash-ai",
		Manifest: def.Manifest,
		// Assets is what the MULTICALL build serves the FE bundle from:
		// a linked-in app skips the exec-probe, so the router reads
		// index.js out of this fs.FS rather than from a probe envelope.
		// Omitting it registered the app, started it, created its window
		// — and then served no bundle, so the custom element was never
		// defined and nothing rendered. Standalone builds probe and were
		// unaffected, which is why only the multicall e2e failed.
		Assets: def.Assets,
		Run:    run,
	})
	managerDef = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID: "com.wash.agents", Name: "Agents", Version: version.Version,
			ProtocolVersion: sdk.ProtocolVersion, Element: "wash-app-agents",
			Surface: sdk.SurfaceWindow, Icon: aiIcon, Accent: "violet",
			Instancing:   sdk.InstancingSingleton,
			Capabilities: []string{sdk.CapSpawn},
			Window:       &sdk.WindowHints{DefaultWidth: 760, DefaultHeight: 600},
		},
		Assets: sub,
		OnReady: func(c *sdk.Conn, instanceID string, windowID uint32) {
			managerMode = true
			onReady(c, instanceID, windowID)
		},
		OnAppMsg: onAppMsg, OnAppMsgFrom: onAppMsgFrom,
		OnCloseRequested: onCloseRequested,
	}
	registry.Register(&registry.App{Name: "wash-agents", Manifest: managerDef.Manifest, Assets: managerDef.Assets, Run: func(ctx context.Context) error { return sdk.Run(ctx, managerDef) }})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

// AgentsDef is the singleton manager surface. It deliberately shares the FE
// bundle with the session window; agentd sends the role at startup and the
// bundle renders only the manager half.
func AgentsDef() *sdk.AppDef { return managerDef }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }

// Launch flags (see parseFlags). Set once at startup, read in onReady.
var (
	flagAgent string
	flagCwd   string
)

// parseFlags reads --agent / --cwd so a session can be started straight
// from a shell:
//
//	wash ai --agent claude --cwd ~/wash
//	wash ai ~/wash                       (agent = first available adapter)
//
// ContinueOnError and a discarded output, matching wash-term: the SDK's
// own argv (--wash-manifest, --open) must pass through unscathed.
//
// The directory is resolved to an absolute path HERE, in the process the
// user launched, because "." and "~" mean something in this cwd and
// nothing in the router's — resolving them later cost a bug already.
func parseFlags() {
	flags := flag.NewFlagSet("wash-ai", flag.ContinueOnError)
	agent := flags.String("agent", "", "which agent to start, on its own defaults (claude, codex, gemini, opencode)")
	cwd := flags.String("cwd", "", "working directory for the session (default: $HOME)")
	flags.SetOutput(io.Discard)
	_ = flags.Parse(os.Args[1:])

	flagAgent = *agent
	dir := *cwd
	if dir == "" && flags.NArg() > 0 {
		dir = flags.Arg(0)
	}
	if dir == "" {
		return
	}
	if strings.HasPrefix(dir, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
		}
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	flagCwd = dir
	if flagAgent == "" {
		// A bare directory still means "start something here" — the
		// launcher would otherwise open with the folder filled in and
		// nothing chosen, which is a worse answer than picking the first
		// adapter that is actually installed.
		flagAgent = firstAvailableAgent()
	}
}

// firstAvailableAgent is the adapter probe's first usable row, so
// `wash ai ~/wash` starts something rather than asking.
func firstAvailableAgent() string {
	for _, a := range agentd.Probe(agentpolicy.Load(agentpolicy.Path())) {
		if a.Available {
			return a.ID
		}
	}
	return ""
}

// session is what this window is currently attached to. One window, one
// session — hence a package-level value rather than a map.
var session struct {
	key   string
	title string
	// cwd is the session's folder, from its roster row: what the editor is
	// rooted at and what transcript paths resolve against.
	cwd string
	// attention mirrors what we last told the router, so a roster push
	// every second doesn't become a wire frame every second (docs/
	// AGENT_UX.md N6).
	attention bool
}

// Messages this backend sends its own FE, besides relaying agentd's pushes
// (the TypeScript twin is WindowMessage in main.tsx).
type (
	roleMsg struct {
		Kind string `json:"kind"`
		Role string `json:"role"`
	}
	autostartMsg struct {
		Kind  string `json:"kind"`
		Agent string `json:"agent"`
		Cwd   string `json:"cwd"`
	}
	startedMsg struct {
		Kind string `json:"kind"`
		Key  string `json:"key"`
	}
	draftMsg struct {
		Kind string `json:"kind"`
		Text string `json:"text"`
	}
	bareMsg struct {
		Kind string `json:"kind"`
	}
)

// persistSessionView records which agentd session this window renders. This
// is backend-owned attachment state, so persist it when agentd confirms the
// attachment rather than relying on a later debounced FE callback. The shell
// returns it as wash:state whenever the browser remounts this instance.
func persistSessionView(c *sdk.Conn) {
	view := map[string]any{"session_key": session.key}
	if err := c.SaveState(view); err != nil {
		log.Printf("wash-ai: persist session key=%s: %v", session.key, err)
	}
}

// bind makes key the session this window shows: remembered across a
// reload, claimed as this window's own, and watched. replay asks for the
// whole transcript again even if agentd thinks this window has it.
func bind(c *sdk.Conn, key string, replay bool) {
	session.key = key
	persistSessionView(c)
	// Before the snapshot can arrive: started clears the FE's event list.
	c.SendAppMsg(startedMsg{Kind: "started", Key: key})
	_ = agentproto.SendAgentd(c, agentproto.SessionClaim{Key: key})
	_ = agentproto.SendAgentd(c, agentproto.TranscriptSubscribe{Key: key, Replay: replay})
}

// markAttention keeps the taskbar pill honest about this window: pulsing
// while its agent is blocked on a person, quiet otherwise. The router
// clears the flag by itself the moment the window is focused, so this
// only ever has to raise it — and only when the answer changed.
func markAttention(c *sdk.Conn, on bool) {
	if session.attention == on {
		return
	}
	session.attention = on
	if err := c.Attention(on); err != nil {
		log.Printf("wash-ai: attention key=%s on=%v: %v", session.key, on, err)
	}
}

// followRoster keeps the window title and taskbar attention in step with
// this window's session.
func followRoster(c *sdk.Conn, state agentproto.State) {
	for _, r := range state.Rows {
		// The window title follows the agent's own name for the session.
		// A taskbar full of "Agent" is unreadable the moment there are
		// three of them; "Fix the reconnect banner race" is not.
		if r.Key == session.key && r.Title != "" && r.Title != session.title {
			session.title = r.Title
			_ = c.SetTitle(r.Title)
		}
		if r.Key == session.key && r.Cwd != "" {
			session.cwd = r.Cwd
		}
	}
	waiting := false
	for _, a := range state.Asks {
		waiting = waiting || session.key != "" && a.RowKey == session.key
	}
	markAttention(c, waiting)
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	// Parsed HERE, not in init(): the multicall binary links every app
	// into one process, so an init() that reads os.Args interprets the
	// DISPATCHER's argv. `wash list-apps` was being read as
	// `wash-ai list-apps` — which then ran an adapter probe, shelling out
	// and logging, on every multicall invocation of every app.
	if !managerMode {
		parseFlags()
	}
	log.Printf("wash-ai ready instance=%s manager=%v", instanceID, managerMode)
	c.SendAppMsg(roleMsg{Kind: "role", Role: map[bool]string{true: "manager", false: "session"}[managerMode]})
	// The launcher picks a working directory with the shared
	// <FilePicker mode="directory">, which talks to its own BE rather than
	// a service. Typing a path into a text field was the placeholder, and
	// it produced the first real bug of the branch (an unexpanded ~).
	sdk.EnableFilePicker(c)
	// Empty root = unconfined, matching fm/edit/imageview. NOT "/", which
	// is a degenerate sandbox that rejects every real path.
	aiFS = wfs.New(c.Session().Root)
	// The manager's roster view feeds the launcher and the sessions pane.
	if managerMode {
		_ = agentproto.SendAgentd(c, agentproto.ManagerSubscribe{})
	}
	// ONE transcript keepalive per window, from the start, whatever later
	// sets the key: bind, from any of the paths that attach a session.
	if !managerMode {
		go keepWatching(c)
	}
	if flagAgent == "" {
		return
	}
	// Launched with flags: skip the launcher entirely. The FE is told
	// first so it shows what is starting instead of flashing an empty
	// form that is about to be replaced.
	c.SendAppMsg(autostartMsg{Kind: "autostart", Agent: flagAgent, Cwd: flagCwd})
	_ = agentproto.SendAgentd(c, agentproto.AgentStart{Agent: flagAgent, Cwd: flagCwd, Claim: true})
}

// onAppMsg handles messages from this window's own FE. agentd's requests
// go to agentd as they are: this window is a thin host, and agentd decides
// what each may do (docs/AGENT_PROTOCOL.md, Trust and roles). What remains
// here is the window's own business.
func onAppMsg(c *sdk.Conn, win uint32, data any) {
	m, _ := data.(map[string]any)
	if m == nil {
		return
	}
	kind := str(m["kind"])
	if _, ok := agentproto.Lookup(agentproto.Request, kind); ok {
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, data)
		return
	}
	switch kind {
	case "restore":
		// A browser reload remounts only the FE; this process and agentd keep
		// running. The persisted key tells the new FE which view it had, while
		// this backend remains authoritative about whether that attachment is
		// still valid. Re-claiming is idempotent for the owner and answers
		// with the session's row and asks, which the remounted FE has never
		// seen.
		key := str(m["key"])
		if key == "" || key != session.key {
			_ = c.SaveState(nil)
			c.SendAppMsg(bareMsg{Kind: "restore_failed"})
			return
		}
		bind(c, key, true)

	case "detach":
		// Leave the session running. agentd keeps its roster row, which
		// is where the user gets back to it.
		finishClose(c, agentproto.AgentDetach{Key: session.key})

	case "terminate":
		finishClose(c, agentproto.AgentStop{Key: session.key})

	case "open_agents":
		_ = c.SpawnRequest("com.wash.agents")

	case "save_transcript":
		// Written through internal/fs, so the sandbox root the router
		// handed this app is what bounds it — the same fence the file
		// picker that chose the path obeys.
		path, text := str(m["path"]), str(m["text"])
		if path == "" {
			return
		}
		abs, n, err := aiFS.Write(path, []byte(text), maxTranscriptBytes)
		if err != nil {
			log.Printf("wash-ai: save transcript %q: %v", path, err)
			c.Fail("Could not save the transcript", err)
			return
		}
		log.Printf("wash-ai: transcript saved path=%s bytes=%d", abs, n)
		c.Info("Transcript saved", abs)

	case "open_terminal", "open_file_manager", "open_text_editor":
		// Project shortcuts use fixed app IDs and a confined directory. Never
		// let a frontend supply an arbitrary application or launch argument.
		target := map[string]string{"open_terminal": "term", "open_file_manager": "fm", "open_text_editor": "edit"}[kind]
		label := map[string]string{"term": "terminal", "fm": "file manager", "edit": "text editor"}[target]
		dir := str(m["cwd"])
		if dir == "" {
			return
		}
		abs, err := aiFS.Confine(dir)
		if err != nil {
			log.Printf("wash-ai: open %s %q: %v", label, dir, err)
			return
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			log.Printf("wash-ai: open %s: not a directory: %s", label, abs)
			return
		}
		log.Printf("wash-ai: open %s cwd=%s", label, abs)
		if err := c.SpawnRequestOpen("com.wash."+target, abs); err != nil {
			log.Printf("wash-ai: spawn %s %s: %v", target, abs, err)
		}

	case "path_probe":
		var paths []string
		if raw, ok := m["paths"].([]any); ok {
			for _, p := range raw {
				paths = append(paths, str(p))
			}
		}
		probePaths(c, str(m["id"]), paths)

	case "editor_show":
		showEditor(c, str(m["token"]))
	}
}

// onAppMsgFrom handles messages from other apps: agentd's pushes, relayed
// to the FE as they are, and agent_draft. The sender is router-attested,
// so a message claiming to be agentd actually is one.
func onAppMsgFrom(c *sdk.Conn, win uint32, data any, from wire.Sender) {
	m, _ := data.(map[string]any)
	if m == nil {
		return
	}
	// agent_draft is the one message this app takes from an app that is
	// not agentd — anything with a selection worth handing to an agent
	// (wash-edit today). Checked BEFORE the sender gate, which exists to
	// stop another app impersonating the roster service, not to stop
	// another app typing into a composer the person is looking at.
	if str(m["kind"]) == draftKind {
		text := str(m["text"])
		if text == "" {
			return
		}
		log.Printf("wash-ai: draft from=%s bytes=%d", from.AppID, len(text))
		c.SendAppMsg(draftMsg{Kind: "draft", Text: text})
		return
	}
	// This window's editor, closing: forget it now rather than when the
	// router reaps it (editor.go).
	if from.AppID == editAppID && str(m["kind"]) == "editor.closing" {
		onInstanceGone(c, from.AppID, from.InstanceID)
		return
	}
	if from.AppID != agentdAppID {
		return
	}
	kind, key := str(m["kind"]), str(m["key"])
	spec, ok := agentproto.Lookup(agentproto.Push, kind)
	if !ok {
		return
	}
	// The pushes that are about this window rather than for its FE.
	switch kind {
	case "detach":
		// The session was detached elsewhere (the rail, the manager).
		if key == session.key {
			log.Printf("wash-ai: detached by agentd key=%s, exiting", session.key)
			os.Exit(0)
		}
		return
	case "claim_denied":
		log.Printf("wash-ai: controller already exists for key=%s; closing duplicate", key)
		os.Exit(0)
	case "wash.focus":
		// The human activated a notification about this session.
		if key == session.key {
			if err := c.Raise(); err != nil {
				log.Printf("wash-ai: raise key=%s: %v", session.key, err)
			}
		}
		return
	case "attach":
		// A window agentd opened for a session: it is ours.
		bind(c, key, false)
		return
	case "agent_started":
		// This window's own start (wash ai --agent) makes it the session's
		// window; the manager's opens elsewhere. Both go on to the FE,
		// which reports a failure either way.
		var started agentproto.AgentStarted
		if agentproto.Decode(data, &started) == nil && started.Error == "" && !managerMode {
			bind(c, started.Key, false)
		}
	case "state", "manager_state", "session_state":
		var view agentproto.RosterState
		if agentproto.Decode(data, &view) == nil {
			followRoster(c, view.State)
		}
		if aiDebug {
			log.Printf("wash-ai: roster push kind=%s key=%q", kind, session.key)
		}
	}
	// A keyed push is about one session; it is this window's business only
	// when that session is the one it shows. The FE guards the same way,
	// because a Bulk frame can overtake the switch it predates.
	if spec.Keyed && key != session.key {
		return
	}
	if spec.Class == agentproto.Bulk {
		c.SendAppMsgBulk(data)
	} else {
		c.SendAppMsg(data)
	}
}

// onCloseRequested vetoes the close and asks the FE what to do with the
// session behind it.
//
// Closing this window does NOT end the session — agentd owns the adapter
// process, not this app — so silently letting the window go would leave
// an agent running with nothing pointing at it. The choice is the user's:
// detach (it keeps working, reattach from the sidebar) or terminate (it
// ends and joins the history).
//
// With no session there is nothing to ask about, so the close is allowed.
func onCloseRequested(c *sdk.Conn, win uint32) bool {
	if managerMode {
		return true
	}
	if session.key == "" {
		return true
	}
	c.SendAppMsg(bareMsg{Kind: "confirm_close"})
	return false
}

// keepWatching re-affirms this window's transcript subscription.
//
// agentd removes watchers on router instance.gone, and also expires one it
// has not heard from as a backstop. Same TTL+keepalive shape the roster's
// own rows use. Started exactly once, from onReady; it re-affirms whatever
// key the window holds at each tick, so it does not matter which path
// (start, attach, restore) set it.
func keepWatching(c *sdk.Conn) {
	t := time.NewTicker(agentd.WatcherRefresh)
	defer t.Stop()
	for {
		select {
		case <-c.Done():
			return
		case <-t.C:
			if session.key == "" {
				continue
			}
			_ = agentproto.SendAgentd(c, agentproto.TranscriptSubscribe{Key: session.key})
		}
	}
}

// finishClose tells agentd what to do with the session — detach or stop —
// then ends this process, which is what actually closes the window.
//
// ConfirmClose(true) does not work here: the close request was already
// answered (with a veto) before the dialog was shown, so a later
// confirmation has nothing to answer and the window stays until the user
// clicks the X a second time. DestroyWindow is documented as a no-op for
// an app's primary window, which "dies with the instance" — so ending the
// instance IS the close.
//
// The message is written synchronously and its error checked before
// exiting, so the bytes are in the socket before this process goes away.
// Nothing local needs cleaning up: agentd owns the adapter, not us.
func finishClose(c *sdk.Conn, verb any) {
	if err := agentproto.SendAgentd(c, verb); err != nil {
		// Could not tell agentd — better to leave the window open than to
		// vanish having neither detached nor terminated.
		log.Printf("wash-ai: %T: %v", verb, err)
		c.Warn("Could not close this session", err.Error())
		return
	}
	log.Printf("wash-ai: %T key=%s, exiting", verb, session.key)
	os.Exit(0)
}

// str reads a string field of a message the SDK handed over as generic
// JSON; anything else reads as empty.
func str(v any) string {
	s, _ := v.(string)
	return s
}
