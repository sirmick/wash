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
	"io/fs"
	"log"

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
	case "agent_started":
		if e := str(m["error"]); e != "" {
			c.SendAppMsg(map[string]any{"kind": "start_failed", "error": e})
			return
		}
		session.key = str(m["key"])
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
		// The roster push carries adapters (for the launcher), this
		// session's row (for the status line) and the pending questions.
		c.SendAppMsg(map[string]any{
			"kind":  "roster",
			"key":   session.key,
			"state": m["state"],
		})
	}
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
