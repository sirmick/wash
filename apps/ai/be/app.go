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

	agentd "github.com/sirmick/wash/apps/agentd/be"
	"github.com/sirmick/wash/internal/apps/registry"
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

var def *sdk.AppDef

func init() {
	parseFlags()
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
		Assets:       sub,
		OnReady:      onReady,
		OnAppMsg:     onAppMsg,
		OnAppMsgFrom: onAppMsgFrom,
	}
	registry.Register(&registry.App{
		Name:     "wash-ai",
		Manifest: def.Manifest,
		Run:      run,
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
}

func onReady(c *sdk.Conn, instanceID string, windowID uint32) {
	log.Printf("wash-ai ready instance=%s", instanceID)
	// The launcher picks a working directory with the shared
	// <FilePicker mode="directory">, which talks to its own BE rather than
	// a service. Typing a path into a text field was the placeholder, and
	// it produced the first real bug of the branch (an unexpanded ~).
	sdk.EnableFilePicker(c)
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
		closing = true
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "agent_detach",
			"key":  session.key,
		})
		_ = c.ConfirmClose(c.WindowID(), true)

	case "terminate":
		closing = true
		_ = c.SendAppMsgTo(wire.Recipient{AppID: agentdAppID}, map[string]any{
			"kind": "agent_stop",
			"key":  session.key,
		})
		_ = c.ConfirmClose(c.WindowID(), true)

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
	if session.key == "" || closing {
		return true
	}
	c.SendAppMsg(map[string]any{"kind": "confirm_close"})
	return false
}

// closing is set once the user has answered, so the second close request
// (the one we ask for ourselves) goes straight through.
var closing bool

func str(v any) string {
	s, _ := v.(string)
	return s
}
