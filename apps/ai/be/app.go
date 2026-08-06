// Package ai is wash-ai (com.wash.ai) — a window onto one managed agent
// session (docs/AGENT_APP.md §9).
//
// It is a thin host. agentd owns the session, the transcript, the roster
// and the approval queue; this app owns a window, a subscription and a
// composer. Everything it does is a message to com.wash.agentd:
//
//	FE → ai   start    {agent, cwd, prompt?}   → ai → agentd  agent_start
//	FE → ai   prompt   {text}                  → ai → agentd  agent_prompt
//	FE → ai   answer   {id, decision, rule?}   → ai → agentd  agent_answer
//	          agentd → ai  transcript_snapshot / transcript_event / state
//	          ai → FE      snapshot / event / status / adapters
//
// The empty window is the launcher: an app with no session yet renders the
// form. That is why there is no separate "new session" dialog anywhere.
//
// InstancingMulti on purpose — one window per session. The desktop is the
// session switcher (taskbar pills, the attention badge, the roster), so
// this app has no tab strip and no session list of its own.
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
	"github.com/sirmick/wash/internal/apps/registry"
	wfs "github.com/sirmick/wash/internal/fs"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/version"
	"github.com/sirmick/wash/internal/wire"
)

//go:embed all:assets
var assetsFS embed.FS

// aiIcon — Lucide sprite symbol; added to web/shell/build-icons.mjs.
const aiIcon = "bot"

const agentdAppID = agentd.AppID

// aiDebug traces the roster subscription, which is what drives the status
// line and the working spinner. Off unless WASH_AGENT_DEBUG is set.
var aiDebug = os.Getenv("WASH_AGENT_DEBUG") != ""

// maxTranscriptBytes bounds a saved transcript. Generous — a long
// session with images is large — but finite, because the FE hands us the
// bytes and a bound is cheaper than trusting it.
const maxTranscriptBytes = 32 << 20

var aiFS *wfs.FS

var def *sdk.AppDef

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
			Capabilities:    []string{},
			Window:          &sdk.WindowHints{DefaultWidth: 620, DefaultHeight: 720},
		},
		Assets:           sub,
		OnReady:          onReady,
		OnAppMsg:         onAppMsg,
		OnAppMsgFrom:     onAppMsgFrom,
		OnCloseRequested: onCloseRequested,
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
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

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
	agent := flags.String("agent", "", "which agent to start (claude, codex, gemini)")
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
	for _, a := range agentd.Probe() {
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
	agent string
	title string
}

// rosterTitle digs this session's title out of the roster push. Defensive
// about shape because the payload crosses the router as generic maps.
func rosterTitle(state any, key string) string {
	s, _ := state.(map[string]any)
	rows, _ := s["rows"].([]any)
	for _, r := range rows {
		row, _ := r.(map[string]any)
		if str(row["key"]) != key {
			continue
		}
		return str(row["title"])
	}
	return ""
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	// Parsed HERE, not in init(): the multicall binary links every app
	// into one process, so an init() that reads os.Args interprets the
	// DISPATCHER's argv. `wash list-apps` was being read as
	// `wash-ai list-apps` — which then ran an adapter probe, shelling out
	// and logging, on every multicall invocation of every app.
	parseFlags()
	log.Printf("wash-ai ready instance=%s", instanceID)
	// The launcher picks a working directory with the shared
	// <FilePicker mode="directory">, which talks to its own BE rather than
	// a service. Typing a path into a text field was the placeholder, and
	// it produced the first real bug of the branch (an unexpanded ~).
	sdk.EnableFilePicker(c)
	// Empty root = unconfined, matching fm/edit/imageview. NOT "/", which
	// is a degenerate sandbox that rejects every real path.
	aiFS = wfs.New(c.Session().Root)
	// Subscribe to the roster so the window can show adapters in the
	// launcher and its own row's state in the status line.
	_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{"kind": sdk.StateServiceKindSubscribe})

	if flagAgent == "" {
		return
	}
	// Launched with flags: skip the launcher entirely. The FE is told
	// first so it shows what is starting instead of flashing an empty
	// form that is about to be replaced.
	c.SendAppMsg(map[string]any{"kind": "autostart", "agent": flagAgent, "cwd": flagCwd})
	_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
		"kind":  "agent_start",
		"agent": flagAgent,
		"cwd":   flagCwd,
	})
}

// onAppMsg handles messages from this window's own FE.
func onAppMsg(c *sdk.Conn, win uint32, data any) {
	m, _ := data.(map[string]any)
	if m == nil {
		return
	}
	switch str(m["kind"]) {
	case "start":
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind":   "agent_start",
			"agent":  str(m["agent"]),
			"cwd":    str(m["cwd"]),
			"prompt": str(m["prompt"]),
		})
	case "prompt":
		if session.key == "" {
			return
		}
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "agent_prompt",
			"key":  session.key,
			"text": str(m["text"]),
		})
	case "detach":
		// Leave the session running. agentd keeps its roster row, which
		// is where the user gets back to it.
		finishClose(c, "agent_detach")

	case "terminate":
		finishClose(c, "agent_stop")

	case "set_mode":
		if session.key == "" {
			return
		}
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "agent_set_mode",
			"key":  session.key,
			"mode": str(m["mode"]),
		})

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

	case "resume":
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind":       "agent_resume",
			"session_id": str(m["session_id"]),
		})

	case "set_config":
		if session.key == "" {
			return
		}
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind":  "agent_set_config",
			"key":   session.key,
			"id":    str(m["id"]),
			"value": str(m["value"]),
		})

	case "cancel":
		if session.key == "" {
			return
		}
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "agent_cancel",
			"key":  session.key,
		})

	case "answer":
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind":     "agent_answer",
			"id":       str(m["id"]),
			"decision": str(m["decision"]),
			"remember": str(m["rule"]) != "",
			"rule":     str(m["rule"]),
		})
	}
}

// onAppMsgFrom handles messages from agentd. The sender is router-attested,
// so a message claiming to be the roster service actually is one.
func onAppMsgFrom(c *sdk.Conn, win uint32, data any, from wire.Sender) {
	if from.AppID != agentdAppID {
		return
	}
	m, _ := data.(map[string]any)
	if m == nil {
		return
	}
	switch str(m["kind"]) {
	case "attach":
		// A reopened session (Resume): agentd already loaded it and is
		// handing us the key. The transcript is already populated
		// service-side by the replay, so subscribing fetches it whole.
		session.key = str(m["key"])
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "transcript_subscribe",
			"key":  session.key,
		})
		c.SendAppMsg(map[string]any{"kind": "started", "key": session.key})
		go keepWatching(c)

	case "agent_started":
		if e := str(m["error"]); e != "" {
			c.SendAppMsg(map[string]any{"kind": "start_failed", "error": e})
			return
		}
		session.key = str(m["key"])
		if aiDebug {
			log.Printf("wash-ai: session started key=%s", session.key)
		}
		// Watch this session's transcript — a separate subscription from
		// the roster, deliberately (see agentd/transcript.go).
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "transcript_subscribe",
			"key":  session.key,
		})
		c.SendAppMsg(map[string]any{"kind": "started", "key": session.key, "session_id": str(m["session_id"])})
		go keepWatching(c)

	case "transcript_snapshot":
		if str(m["key"]) != session.key {
			return
		}
		c.SendAppMsg(map[string]any{"kind": "snapshot", "events": m["events"]})

	case "transcript_event":
		if str(m["key"]) != session.key {
			return
		}
		c.SendAppMsg(map[string]any{"kind": "event", "event": m["event"]})

	case sdk.StateServiceKindState:
		// The window title follows the agent's own name for the session.
		// A taskbar full of "Agent" is unreadable the moment there are
		// three of them; "Fix the reconnect banner race" is not.
		if t := rosterTitle(m["state"], session.key); t != "" && t != session.title {
			session.title = t
			_ = c.SetTitle(t)
		}
		if aiDebug {
			log.Printf("wash-ai: roster push key=%q state=%T", session.key, m["state"])
		}
		// The roster push carries adapters (for the launcher), this
		// session's row (for the status line) and the pending questions.
		c.SendAppMsg(map[string]any{
			"kind":  "roster",
			"key":   session.key,
			"state": m["state"],
		})
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
	if session.key == "" {
		return true
	}
	c.SendAppMsg(map[string]any{"kind": "confirm_close"})
	return false
}

// keepWatching re-affirms this window's transcript subscription.
//
// agentd expires a watcher it has not heard from, because a closed window
// cannot be detected at send time — there is no app-gone signal and
// SendAppMsgTo does not fail for a dead instance. Same TTL+keepalive
// shape the roster's own rows use.
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
			_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
				"kind": "transcript_subscribe",
				"key":  session.key,
			})
		}
	}
}

// finishClose tells agentd what to do with the session, then ends this
// process — which is what actually closes the window.
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
func finishClose(c *sdk.Conn, verb string) {
	if err := c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
		"kind": verb,
		"key":  session.key,
	}); err != nil {
		// Could not tell agentd — better to leave the window open than to
		// vanish having neither detached nor terminated.
		log.Printf("wash-ai: %s: %v", verb, err)
		c.Warn("Could not close this session", err.Error())
		return
	}
	log.Printf("wash-ai: %s key=%s, exiting", verb, session.key)
	os.Exit(0)
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
