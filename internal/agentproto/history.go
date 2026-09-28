package agentproto

// History and administration: remembered sessions, found, renamed,
// deleted, pruned and reopened; and the connection keys the launcher
// stores.

// SessionMeta is what the history panel lists. Assembled from a
// transcript's head and tail without reading the conversation in
// between — a history list must not cost the sum of every transcript.
type SessionMeta struct {
	SessionID  string `json:"session_id"`
	Agent      string `json:"agent,omitempty"`
	Connection string `json:"connection,omitempty"`
	// Catalog and LaunchModel are how the session was started (the model
	// as asked: a slot name or an id), for starting another the same way;
	// Model is what it actually ran, from its summary.
	Catalog     string `json:"catalog,omitempty"`
	LaunchModel string `json:"launch_model,omitempty"`
	Model       string `json:"model,omitempty"`
	Cwd         string `json:"cwd,omitempty"`
	Dir         string `json:"dir,omitempty"`
	Title       string `json:"title,omitempty"`
	// UserTitle is the person's name for the session, when they gave one.
	// Title above is then the SAME string — the effective title, so every
	// reader shows the name without knowing where it came from — and this
	// field says it was theirs.
	UserTitle string `json:"user_title,omitempty"`
	// Parent is the session that launched this one: the orchestrator of
	// its workspace, or the member that spawned it. Empty for a session a
	// person started.
	Parent    string `json:"parent,omitempty"`
	StartedMS int64  `json:"started_ms,omitempty"`
	EndedMS   int64  `json:"ended_ms,omitempty"`
	EndReason string `json:"end_reason,omitempty"`
	Events    int    `json:"events,omitempty"`
	// Bytes is the transcript's size on disk, so the UI can say what
	// history costs and offer to prune the expensive ones.
	Bytes int64 `json:"bytes,omitempty"`
	// Preview is a few recent human/agent lines taken from the same bounded
	// tail read used for the metadata. It gives the always-visible history
	// list enough context without loading or sending whole transcripts.
	Preview string `json:"preview,omitempty"`
	// Snippet is the line that matched, with a little either side. Absent
	// when the query matched metadata instead (the row already shows the
	// title and directory, so quoting them back is noise) or when there
	// was no query at all.
	Snippet string `json:"snippet,omitempty"`
	// Live / Detached / RowKey are stamped on the way out from the
	// roster, not read from the file — the transcript index knows what a
	// session WAS, and only the roster knows what it is doing now.
	//
	// The panel did not have these at all, so it would happily offer to
	// resume a session that was already running: the precise duplication
	// the menu's filter exists to prevent, in the view that had no
	// filter. Same predicate, both views (see rosterIndex).
	Live     bool   `json:"live,omitempty"`
	Detached bool   `json:"detached,omitempty"`
	RowKey   string `json:"row_key,omitempty"`
}

// AgentHistory searches the stored sessions: their metadata and, with a
// query, their conversations.
type AgentHistory struct {
	Query string `json:"query,omitempty"`
	// Limit bounds the answer; 0 and anything above 200 mean 200.
	Limit int `json:"limit,omitempty"`
	// All includes the sessions a workspace launched (its members). Without
	// it the answer is top-level sessions only: one orchestrator can launch
	// dozens of members, and they buried the conversations people started.
	All bool `json:"all,omitempty"`
}

// History answers AgentHistory, newest first, each session stamped with
// what the roster says about it now.
type History struct {
	Query    string        `json:"query"`
	Sessions []SessionMeta `json:"sessions"`
}

// AgentResume reopens a stored session: session/load replays it, and an
// Agent window opens on it (or the one already showing it comes forward).
type AgentResume struct {
	SessionID string `json:"session_id"`
}

// AgentRename names a session, by roster key when it is running or by
// session id when it is not. An empty title clears the person's name and
// the agent's own shows again.
type AgentRename struct {
	Key       string `json:"key,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
}

// AgentDelete deletes a stored session that is not running.
type AgentDelete struct {
	SessionID string `json:"session_id"`
}

// HistoryDeleted answers AgentDelete.
type HistoryDeleted struct {
	SessionID string `json:"session_id"`
	Error     string `json:"error,omitempty"`
}

// AgentPrune deletes stored sessions older than MaxAgeMS.
type AgentPrune struct {
	// MaxAgeMS is how old a session must be to go; 0 means every stored
	// session that is not running. A duration rather than a cutoff so a
	// browser clock on another machine cannot be the one deciding.
	MaxAgeMS int64 `json:"max_age_ms"`
}

// HistoryPruned answers AgentPrune: how many sessions went.
type HistoryPruned struct {
	Deleted int `json:"deleted"`
}

// AgentSetKey stores a connection key (State.Keys), or clears it with an
// empty value. The value is never sent back.
type AgentSetKey struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// KeySaved answers AgentSetKey.
type KeySaved struct {
	Name  string `json:"name"`
	Error string `json:"error,omitempty"`
}

// AgentTestKey checks a key against its provider: the typed value, or the
// stored one when Value is empty.
type AgentTestKey struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// KeyTest answers AgentTestKey with what the provider said.
type KeyTest struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func init() {
	register(Spec{Kind: "agent_history", Dir: Request, Payload: AgentHistory{}, From: "any frontend", Reply: "history",
		Doc: "Search stored sessions."})
	register(Spec{Kind: "agent_resume", Dir: Request, Payload: AgentResume{}, From: "any frontend",
		Doc: "Reopen a stored session in an Agent window."})
	register(Spec{Kind: "agent_rename", Dir: Request, Payload: AgentRename{}, From: "any frontend",
		Doc: "Name a session, or clear the name."})
	register(Spec{Kind: "agent_delete", Dir: Request, Payload: AgentDelete{}, From: "any frontend", Reply: "history_deleted",
		Doc: "Delete a stored session."})
	register(Spec{Kind: "agent_prune", Dir: Request, Payload: AgentPrune{}, From: "any frontend", Reply: "history_pruned",
		Doc: "Delete stored sessions older than an age."})
	register(Spec{Kind: "agent_set_key", Dir: Request, Payload: AgentSetKey{}, From: "a manager (manager_subscribe)", Reply: "key_saved",
		Doc: "Store or clear a connection key."})
	register(Spec{Kind: "agent_test_key", Dir: Request, Payload: AgentTestKey{}, From: "a manager (manager_subscribe)", Reply: "key_test",
		Doc: "Check a key with its provider."})

	register(Spec{Kind: "history", Dir: Push, Payload: History{}, From: "the asker",
		Doc: "Stored sessions matching a history query."})
	register(Spec{Kind: "history_deleted", Dir: Push, Payload: HistoryDeleted{}, From: "the asker",
		Doc: "The outcome of a delete."})
	register(Spec{Kind: "history_pruned", Dir: Push, Payload: HistoryPruned{}, From: "the asker",
		Doc: "The outcome of a prune."})
	register(Spec{Kind: "key_saved", Dir: Push, Payload: KeySaved{}, From: "the asker",
		Doc: "The outcome of storing a key."})
	register(Spec{Kind: "key_test", Dir: Push, Payload: KeyTest{}, From: "the asker",
		Doc: "What the provider said about a key."})
}
