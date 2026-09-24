package agentproto

// The roster state: what agentd publishes through its StateService to every
// subscriber (the session gateway, hostgw), and as keyed views to the Agents
// manager and each Agent window (manager_state, session_state).

// Version is the protocol version, carried on every State. It changes on
// any breaking change to a message in this package; a frontend that does
// not know the version it is sent refuses to render rather than guessing.
// There is no negotiation and no older version kept.
const Version = 1

// State is the public roster. Rows are pre-sorted for display: the agents
// waiting on a human first, then the ones working, then everything else —
// the sidebar is a queue of things to attend to, not a table.
type State struct {
	// Version is always the package's Version (see above).
	Version int   `json:"version"`
	Rows    []Row `json:"rows"`
	// Asks are permission questions waiting for a human (§12). They ride
	// the roster's own push so the sidebar needs no second subscription.
	Asks []Ask `json:"asks,omitempty"`
	// Recent is the remembered session history (§13) — what a reboot or a
	// closed window would otherwise have cost you.
	Recent []Session `json:"recent,omitempty"`
	// Adapters is which agents this box can actually launch over ACP
	// (docs/AGENT_APP.md §6). The launcher renders unavailable ones greyed
	// with their reason rather than hiding them, so "why can I not pick
	// Claude here" has an answer on screen.
	Adapters []Adapter `json:"adapters,omitempty"`
	// Stacks are what the launcher offers first (stacks.go): each with the
	// availability of its tiers, greyed with a reason like an adapter.
	Stacks []StackView `json:"stacks,omitempty"`
	// Keys are the connection keys the launcher can store: set or not, and
	// a stored key's last four characters. Never a value.
	Keys []KeyView `json:"keys,omitempty"`
	// HasDefaultPrompt says whether a stored default prompt exists, so the
	// launcher can say that a new session will not start empty. Only the
	// FLAG rides the state push — the text itself is fetched on demand
	// (agent_default prompt), because a page of prose on every roster push
	// would reach every subscriber several times a second during a turn.
	HasDefaultPrompt bool `json:"has_default_prompt,omitempty"`
}

// Row is one hosted agent session.
type Row struct {
	// Key identifies the session on the roster: "acp:<n>", minted by
	// agentd. Every session-scoped message names it.
	Key string `json:"key"`
	// Agent is the slug ("claude", "codex", …).
	Agent string `json:"agent"`
	// State is running | working | needs-input | done | failed, or
	// "stale" once an exited session's row is ageing out.
	State string `json:"state"`
	// Reason qualifies the state: which input is wanted (permission |
	// idle), or how a turn or session ended (cancelled, error, exited, …).
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
	// SinceMS is how long the row has been in this state, as of the push.
	// The FE anchors its own clock to it (no cross-clock comparison).
	SinceMS int64 `json:"since_ms"`
	// Used / Size are the agent's context accounting (usage_update), and
	// Title is its own name for the session (session_info_update).
	Used  int64  `json:"used,omitempty"`
	Size  int64  `json:"size,omitempty"`
	Title string `json:"title,omitempty"`
	// Preview is populated only in the compact manager projection. Live
	// transcript changes arrive as bounded Bulk patches, never by
	// republishing the complete roster.
	Preview string `json:"preview,omitempty"`
	// Mode is the agent's active approval preset and Modes what it offers
	// (docs/AGENT_APP.md §9). Empty for an agent with no such notion.
	Mode  string `json:"mode,omitempty"`
	Modes []Mode `json:"modes,omitempty"`
	// Yolo is wash answering this session's permission questions with
	// "allow" instead of asking. Host-side and per-session; it is on the
	// row so every surface that shows the session can SAY so — an
	// auto-approving agent that looks like any other is the failure mode
	// this flag exists to prevent.
	Yolo bool `json:"yolo,omitempty"`
	// Configs is the agent's generic settings block (model, reasoning
	// effort, plan mode…). Commands are its own slash commands.
	Configs  []Config  `json:"configs,omitempty"`
	Commands []Command `json:"commands,omitempty"`
	// Detached marks a session still running with no window pointing at
	// it — the sidebar offers Reattach rather than focus.
	Detached bool `json:"detached,omitempty"`
	// Queued is how many prompts are waiting for the current turn to end
	// (docs/AGENT_MESSENGER.md semantics: a message typed mid-reply is
	// sent when the reply finishes, not dropped and not interleaved).
	Queued int `json:"queued,omitempty"`
	// Roots are folders this session may reach BEYOND its cwd (roots.go).
	// On the row because every surface that shows a session must be able
	// to say how wide it is: a session with three extra roots is a
	// different thing from one confined to its own folder, and only the
	// person who widened it would otherwise know.
	Roots []string `json:"roots,omitempty"`
	// Stale marks the row of a session that is no longer hosted: shown
	// greyed, then dropped.
	Stale bool `json:"stale,omitempty"`
	// Workspace places the session in a workspace team, so the Agents
	// window lists members under the orchestrator that leads them. Set
	// only on the manager's projection.
	Workspace *RowWorkspace `json:"workspace,omitempty"`
}

// RowWorkspace is one session's place in a workspace team.
type RowWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// LeadSession is the orchestrator's session: a member row nests under
	// the row with this session_id.
	LeadSession  string `json:"lead_session"`
	Orchestrator bool   `json:"orchestrator,omitempty"`
	Member       string `json:"member"`
	Role         string `json:"role,omitempty"`
	Package      string `json:"package,omitempty"`
	PackageTitle string `json:"package_title,omitempty"`
}

// Mode is one approval/sandbox preset an agent offers.
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Config is one agent setting the session can change.
type Config struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Description string        `json:"description,omitempty"`
	Current     string        `json:"current,omitempty"`
	Values      []ConfigValue `json:"values,omitempty"`
}

type ConfigValue struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Command is one slash command the agent offers.
type Command struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Ask is one question waiting for a human. It rides the roster's own
// state push, so the sidebar needs no second subscription.
type Ask struct {
	// ID is agentd's handle for this question; the answer names it.
	ID string `json:"id"`
	// Agent / Tool / Subject are what the human reads: "claude wants to
	// run `git push origin main`".
	Agent   string `json:"agent"`
	Tool    string `json:"tool"`
	Subject string `json:"subject,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	Dir     string `json:"dir,omitempty"`
	// SuggestedRule is what "Always allow" would write. Shown ON the
	// button — what you clicked is what gets saved.
	SuggestedRule string `json:"suggested_rule,omitempty"`
	// RuleCwd is the directory that rule would be confined to, when it
	// would be (agentpolicy.RuleScope): a Bash rule from one project must
	// not buy the same command in every other checkout.
	RuleCwd string `json:"rule_cwd,omitempty"`
	// RowKey ties the question to its roster row (same "<instance>:<chan>"
	// key for terminals; hosted sessions mint their own), so the sidebar
	// can render it against the right agent.
	RowKey string `json:"row_key"`
	// WorkspaceName is set when the asking session is a workspace member,
	// and is what lets the prompt offer "always, for this workspace"
	// alongside the global "always". The ID is deliberately NOT sent: the
	// answer names a scope, never a target (see the answer path).
	WorkspaceName string `json:"workspace_name,omitempty"`
	// AgeMS is how long it has been waiting, as of the push.
	AgeMS int64 `json:"age_ms"`
}

// Session is one remembered agent session.
type Session struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	// Connection is what a resume must launch through again; Stack and
	// Tier are what the launcher defaults to next time (launchRecord).
	Connection string `json:"connection,omitempty"`
	Stack      string `json:"stack,omitempty"`
	Tier       string `json:"tier,omitempty"`
	Cwd        string `json:"cwd,omitempty"`
	Dir        string `json:"dir,omitempty"`
	// Title is what this session was ABOUT, in the agent's own words —
	// it names its sessions on session_info_update once it works out what
	// the work is. "codex · mick" tells you nothing a week later; "Fix
	// the reconnect banner race" does.
	Title string `json:"title,omitempty"`
	// UserTitle is the name a PERSON gave the session (session_admin.go).
	// When set it is what publishHistory puts in Title; the agent's own
	// title stays here underneath so clearing the user's falls back to it.
	UserTitle string `json:"user_title,omitempty"`
	// LastSeen is unix seconds — an absolute the FE renders as "2h ago",
	// and the only field a keepalive touches.
	LastSeen int64 `json:"last_seen"`
	// Live is set on the way out to the FE: a session whose agent is
	// running right now is in the roster above, so the Recent list greys
	// it rather than offering to resume what is already here.
	Live bool `json:"live,omitempty"`
	// Detached is a live session with no window pointing at it.
	//
	// Live and REACHABLE are not the same thing, and treating them as one
	// is what made the History menu useless in exactly the case you open
	// it for. agent_detach sets the flag and closes the window but never
	// retires the row, so a detached session is still "live" — and the
	// menu, which hides live sessions to avoid offering to duplicate a
	// running one, hid the one thing you were trying to get back.
	//
	// A detached session is not something to resume. It is something to
	// reattach to, which is a different verb with a different outcome.
	Detached bool `json:"detached,omitempty"`
	// RowKey is the roster key this session is running as, present only
	// while it has a row. Reattach is key-addressed, not session-id
	// addressed, so the menu needs this to offer the verb at all.
	RowKey string `json:"row_key,omitempty"`
}

// StackView is a stack as the launcher shows it.
type StackView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Available is every tier startable here; Note says why not.
	Available bool       `json:"available"`
	Note      string     `json:"note,omitempty"`
	Tiers     []TierView `json:"tiers,omitempty"`
}

// TierView is one tier as the launcher shows it.
type TierView struct {
	Tier       string `json:"tier"`
	Adapter    string `json:"adapter"`
	Connection string `json:"connection,omitempty"`
	Model      string `json:"model,omitempty"`
	Thinking   string `json:"thinking,omitempty"`
	Capability string `json:"capability,omitempty"`
	// ReadOnly is set on the review tier: "enforced" where the adapter's
	// tools are restricted (Claude Code's reviewer capability), otherwise
	// "instruction" — the reviewer is asked not to write, and could.
	ReadOnly  string `json:"read_only,omitempty"`
	Available bool   `json:"available"`
	Note      string `json:"note,omitempty"`
}

// KeyView is a key as the launcher shows it.
type KeyView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Set  bool   `json:"set"`
	// Hint is the stored key's last four characters.
	Hint     string `json:"hint,omitempty"`
	Testable bool   `json:"testable,omitempty"`
}

// Adapter is one way to reach an agent over ACP, as the launcher shows it:
// whether this box can start it, and if not, why.
type Adapter struct {
	// ID is what the launcher and the roster call this agent.
	ID string `json:"id"`
	// Name is what a human reads.
	Name string `json:"name"`
	// Note explains a greyed row: why this one cannot be used here.
	Note string `json:"note,omitempty"`
	// Available is whether it can be launched here.
	Available bool `json:"available"`
}
