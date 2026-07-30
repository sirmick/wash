// Package agentd is wash-agentd (com.wash.agentd) — the coding-agent
// roster (docs/AGENT_TERM.md §7). It is a singleton background service
// holding one row per agent wash can see, across every terminal window on
// the box, so the desktop can answer "what are my agents doing?" in one
// place instead of one tab chip at a time.
//
// It owns no agents and talks to none: wash-term is the producer (it is
// the process that owns the pty and therefore the only thing that knows),
// and the session sidebar is the consumer. That is what earns this a
// service rather than a library — N terminal processes producing, the
// sidebar (and later the policy audit) consuming, with no way for the
// producers to see each other otherwise.
//
// Wire shape — inbound from a terminal (cross-app, From router-attested):
//
//	{kind:"agent_status", channel_id, window_id, agent, state, reason,
//	                      session_id, cwd, since_ms}
//	{kind:"agent_gone",   channel_id}
//
// Wire shape — inbound from a subscriber (the session gateway):
//
//	{kind:"subscribe"} / {kind:"unsubscribe"}
//
// Wire shape — state pushed to subscribers (sdk.StateService):
//
//	{kind:"state", state:{ rows:[…] }}
//
// Liveness is the service's own job: a terminal that crashes never says
// goodbye, so rows carry a last-seen stamp, go stale after
// staleAfter, and are dropped after dropAfter. A dead window can leave a
// grey row for a minute; it can never leave a ghost.
package agentd

import (
	"context"

	"github.com/sirmick/wash/internal/apps/registry"
	"github.com/sirmick/wash/internal/sdk"
	"github.com/sirmick/wash/internal/version"
)

// AppID is the reserved app id for the agent roster service.
const AppID = "com.wash.agentd"

// State is the public roster. Rows are pre-sorted for display: the agents
// waiting on a human first, then the ones working, then everything else —
// the sidebar is a queue of things to attend to, not a table.
type State struct {
	Rows []Row `json:"rows"`
}

// Row is one agent in one terminal tab.
type Row struct {
	// Key identifies the tab: "<term instance>:<channel id>". The router
	// attests the instance, so a terminal cannot forge another's rows.
	Key string `json:"key"`
	// Agent is the slug ("claude", "codex", …).
	Agent string `json:"agent"`
	// State is running | working | needs-input | done (as the terminal
	// reported it), or "stale" once liveness has expired.
	State string `json:"state"`
	// Reason qualifies needs-input: permission | idle.
	Reason string `json:"reason,omitempty"`
	// SessionID is the agent's own session id — what makes a row
	// actionable later (`--resume`, copy-session-id).
	SessionID string `json:"session_id,omitempty"`
	// Cwd is where the agent is working; Dir is its basename, which is
	// what a 300px sidebar column can actually show.
	Cwd string `json:"cwd,omitempty"`
	Dir string `json:"dir,omitempty"`
	// Branch / Dirty come from agentd shelling git in Cwd, lazily and
	// cached — never from the agent's hooks (§7).
	Branch string `json:"branch,omitempty"`
	Dirty  bool   `json:"dirty,omitempty"`
	// TermInstance + WindowID address the owning terminal window, so a
	// click on the row can focus it.
	TermInstance string `json:"term_instance"`
	WindowID     uint64 `json:"window_id"`
	ChannelID    uint64 `json:"channel_id"`
	// SinceMS is how long the row has been in this state, as of the push.
	// The FE anchors its own clock to it (no cross-clock comparison).
	SinceMS int64 `json:"since_ms"`
	// Stale marks a row whose terminal stopped reporting: shown greyed,
	// then dropped. See staleAfter / dropAfter.
	Stale bool `json:"stale,omitempty"`
}

var def *sdk.AppDef

func init() {
	def = &sdk.AppDef{
		Manifest: sdk.Manifest{
			ID:              AppID,
			Name:            "Agents",
			Version:         version.Version,
			ProtocolVersion: sdk.ProtocolVersion,
			Surface:         sdk.SurfaceBackground,
			Instancing:      sdk.InstancingSingleton,
		},
		OnReady: onReady,
	}
	registry.Register(&registry.App{
		Name:     "wash-agentd",
		Manifest: def.Manifest,
		Run:      run,
	})
}

// Def is the AppDef for the standalone shim's sdk.Main call.
func Def() *sdk.AppDef { return def }

func run(ctx context.Context) error { return sdk.Run(ctx, def) }
