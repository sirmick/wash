package agentproto

import "github.com/sirmick/wash/internal/swarm"

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
	// Questions are question sets waiting for a human: an adapter's form
	// elicitation (Claude Code's AskUserQuestion) or a workspace member's
	// decision_request. The asker waits for the answer.
	Questions []PendingQuestion `json:"questions,omitempty"`
	// Recent is the remembered session history (§13) — what a reboot or a
	// closed window would otherwise have cost you.
	Recent []Session `json:"recent,omitempty"`
	// Adapters is which agents this box can actually launch over ACP
	// (docs/AGENT_APP.md §6). The launcher renders unavailable ones greyed
	// with their reason rather than hiding them, so "why can I not pick
	// Claude here" has an answer on screen.
	Adapters []Adapter `json:"adapters,omitempty"`
	// Catalogs are what the launcher offers first (catalogs.go): each with
	// the availability of its slots, greyed with a reason like an adapter.
	Catalogs []CatalogView `json:"catalogs,omitempty"`
	// Keys are the connection keys the launcher can store: set or not, and
	// a stored key's last four characters. Never a value.
	Keys []KeyView `json:"keys,omitempty"`
	// Connections are the named ways to reach an adapter (built in, and
	// agents.json's), which a tier may name instead of the adapter direct.
	Connections []ConnectionView `json:"connections,omitempty"`
	// AdapterOptions is what each adapter last reported when a session of
	// it started: its version, approval presets and settings (models,
	// efforts). The Stacks tab and the launcher's Advanced and Permissions
	// controls offer these instead of free text; an adapter that has never
	// run here has no entry, and its fields are typed.
	AdapterOptions []AdapterOptions `json:"adapter_options,omitempty"`
	// Launch is the remembered permission default the launcher starts a
	// session with (agents.json `launch`).
	Launch LaunchPrefs `json:"launch"`
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
	// Background is the work the session left running in the background
	// (a Bash run in the background): what it is, while it runs.
	Background string `json:"background,omitempty"`
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
	// Node and NodeTitle are the plan node the member works on.
	Node      string `json:"node,omitempty"`
	NodeTitle string `json:"node_title,omitempty"`
}

// Mode is one approval/sandbox preset an agent offers.
type Mode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// Config is one agent setting the session can change.
type Config struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Category is the ACP category ("model", "thought_level", …), which is
	// how a setting is matched across adapters that name it differently.
	Category string        `json:"category,omitempty"`
	Current  string        `json:"current,omitempty"`
	Values   []ConfigValue `json:"values,omitempty"`
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

// PendingQuestion is a question set waiting for the human, shown pinned above the
// asking session's composer (and in its workspace tab) until answered.
type PendingQuestion struct {
	// ID is agentd's handle; the answer names it. A workspace decision's is
	// its message id.
	ID string `json:"id"`
	// RowKey is the asking session's roster row.
	RowKey string `json:"row_key"`
	Agent  string `json:"agent,omitempty"`
	// Source is elicitation (the adapter asked) or decision (a workspace
	// member's decision_request).
	Source string            `json:"source"`
	Set    swarm.QuestionSet `json:"set"`
	// WorkspaceName and MemberID place a workspace member's question.
	WorkspaceName string `json:"workspace_name,omitempty"`
	MemberID      string `json:"member_id,omitempty"`
	// AgeMS is how long it has been waiting, as of the push.
	AgeMS int64 `json:"age_ms"`
}

// Session is one remembered agent session.
type Session struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	// Connection is what a resume must launch through again; Catalog and
	// Model are what the launcher defaults to next time (launchRecord).
	Connection string `json:"connection,omitempty"`
	Catalog    string `json:"catalog,omitempty"`
	Model      string `json:"model,omitempty"`
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

// CatalogView is a catalog as the launcher and the Catalog tab show it. A
// catalog is either an adapter's own model list (Adapter set, no Slots:
// what the adapter reports is what the Model select offers) or a curated
// set of three slots, frontier, coding and small, each a model on an
// adapter.
type CatalogView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// Adapter and Connection are an auto catalog's: the adapter whose
	// models it lists, reached direct or through the connection.
	Adapter    string `json:"adapter,omitempty"`
	Connection string `json:"connection,omitempty"`
	// Available is every slot (or the adapter) startable here; Note says
	// why not.
	Available bool       `json:"available"`
	Note      string     `json:"note,omitempty"`
	Slots     []SlotView `json:"slots,omitempty"`
	// Builtin is a catalog wash ships (catalogs.json); Overridden says
	// agents.json changes it. A catalog that is neither is the user's
	// own. The Catalog tab offers "reset" for an overridden built-in and
	// "delete" for the user's own.
	Builtin    bool `json:"builtin,omitempty"`
	Overridden bool `json:"overridden,omitempty"`
}

// SlotView is one slot of a curated catalog: a model on an adapter, with
// its effort. Nothing about permissions (see catalogs.go).
type SlotView struct {
	Slot       string `json:"slot"`
	Adapter    string `json:"adapter"`
	Connection string `json:"connection,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	Available  bool   `json:"available"`
	Note       string `json:"note,omitempty"`
}

// AdapterOptions is what one adapter offered the last time a session of it
// started here (agentd remembers it across restarts).
type AdapterOptions struct {
	// Adapter is the adapter id ("claude", "codex", …).
	Adapter string `json:"adapter"`
	// Version is the adapter's own version string, as it introduced itself.
	Version string `json:"version,omitempty"`
	// Modes are its approval presets; Configs its settings, of which the
	// ones in the "model" and "thought_level" categories are the model and
	// effort lists. Current values are those of the session that reported
	// them and mean nothing here.
	Modes   []Mode   `json:"modes,omitempty"`
	Configs []Config `json:"configs,omitempty"`
}

// ConnectionView is one named connection: which adapter it launches, and
// the key it needs, if any.
type ConnectionView struct {
	ID      string `json:"id"`
	Adapter string `json:"adapter"`
	Key     string `json:"key,omitempty"`
}

// LaunchPrefs is the remembered permission default for a launch.
type LaunchPrefs struct {
	// Mode is the adapter session mode to start in, by adapter id; absent
	// is the adapter's default.
	Mode map[string]string `json:"mode,omitempty"`
	// Yolo starts sessions with host-side auto-approval on.
	Yolo bool `json:"yolo,omitempty"`
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
