package wire

// Activity journal shapes (docs/COMMANDER.md §3). One set of structs is
// shared by the router's store, the SDK's Note, and the shell verbs, so
// an entry that an app noted is byte-for-byte the entry a Timeline row
// renders.

// ActivityIntent is the way back to what an entry describes — exactly the
// shapes the start menu's Recent pop-outs and the agent verbs already
// accept, so a consumer needs no new plumbing to act on a row.
type ActivityIntent struct {
	// Kind is focus | resume | open.
	Kind string `json:"kind"`
	// Origin names the host for focus; empty means the entry's own.
	Origin     string `json:"origin,omitempty"`
	AppID      string `json:"app_id,omitempty"`
	InstanceID string `json:"instance_id,omitempty"`
	WindowID   uint32 `json:"window_id,omitempty"`
	SessionID  string `json:"session_id,omitempty"`
	RowKey     string `json:"row_key,omitempty"`
	Path       string `json:"path,omitempty"`
}

// ActivityEntry is one journal row. (Host, Seq) is its identity.
type ActivityEntry struct {
	TS   int64  `json:"ts"`
	Seq  uint64 `json:"seq"`
	Host string `json:"host"`
	// Kind is dotted and namespaced by source: window.open, open.routed,
	// session.attach, agent.turn, priv.escalate, brief, rollup …
	Kind     string `json:"kind"`
	App      string `json:"app,omitempty"`
	Instance string `json:"instance,omitempty"`
	Window   uint32 `json:"window,omitempty"`
	Title    string `json:"title,omitempty"`
	// Line is plain text, bounded by the store; Truncated says it was cut.
	// Ref is source-defined (a transcript seq, a path) and small.
	Line      string          `json:"line"`
	Truncated bool            `json:"truncated,omitempty"`
	Ref       map[string]any  `json:"ref,omitempty"`
	Intent    *ActivityIntent `json:"intent,omitempty"`
}

// ActivityStats is what About shows: what is stored, and what was dropped.
type ActivityStats struct {
	Enabled bool           `json:"enabled"`
	Days    int            `json:"days"`
	Bytes   int64          `json:"bytes"`
	Dropped uint64         `json:"dropped"`
	Today   map[string]int `json:"today"`
	Seq     uint64         `json:"seq"`
	Path    string         `json:"path,omitempty"`
}

// --- app → router ---

// TEvtActivityNote: an app backend records a fact about itself. The router
// stamps time, host, app id and instance id from the attested sender; a
// note that names a window the instance does not own, or arrives faster
// than the per-instance limit, is dropped and logged. Requires
// CapActivityNote. Fire-and-forget.
const TEvtActivityNote = "activity.note"

// EvtActivityNote is the app half of an ActivityEntry: everything the
// router cannot know itself.
type EvtActivityNote struct {
	T      string          `json:"t"`
	Kind   string          `json:"kind"`
	Win    uint32          `json:"win,omitempty"`
	Title  string          `json:"title,omitempty"`
	Line   string          `json:"line"`
	Ref    map[string]any  `json:"ref,omitempty"`
	Intent *ActivityIntent `json:"intent,omitempty"`
}

func NewEvtActivityNote(kind, line string) EvtActivityNote {
	return EvtActivityNote{T: TEvtActivityNote, Kind: kind, Line: line}
}

// --- shell ↔ router ---

const (
	// Shell → router: a page of the journal, newest first.
	TShellActivityQuery = "activity.query"
	// Router → shell: the page. Cursor is set when more entries precede it.
	TShellActivityQueryOK = "activity.query.ok"
	// Router → shell: the query was refused (journal off, bad cursor).
	TShellActivityQueryErr = "activity.query.err"
	// Shell → router: start (On) or stop pushing new entries as they land.
	TShellActivityTail = "activity.tail"
	// Router → shell: one new entry, telemetry class, while a tail is on.
	TShellActivityEntry = "activity.entry"
	// Shell → router: the store's footprint. Router → shell: the answer.
	TShellActivityStats   = "activity.stats"
	TShellActivityStatsOK = "activity.stats.ok"
	// Shell → router: delete every day file. Router → shell: done.
	TShellActivityClear   = "activity.clear"
	TShellActivityClearOK = "activity.clear.ok"
)

type ShellActivityQuery struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	// From/To bound the entry time (unix ms), inclusive; zero is open.
	From int64 `json:"from,omitempty"`
	To   int64 `json:"to,omitempty"`
	// Kinds match exactly, or by prefix when they end in "."; Apps match
	// exactly; Text is a case-insensitive substring over title and line.
	Kinds  []string `json:"kinds,omitempty"`
	Apps   []string `json:"apps,omitempty"`
	Text   string   `json:"text,omitempty"`
	Limit  int      `json:"limit,omitempty"`
	Cursor string   `json:"cursor,omitempty"`
}

func NewShellActivityQuery(reqID uint64) ShellActivityQuery {
	return ShellActivityQuery{T: TShellActivityQuery, ReqID: reqID}
}

type ShellActivityQueryOK struct {
	T       string          `json:"t"`
	ReqID   uint64          `json:"req_id"`
	Entries []ActivityEntry `json:"entries"`
	Cursor  string          `json:"cursor,omitempty"`
}

func NewShellActivityQueryOK(reqID uint64, entries []ActivityEntry, cursor string) ShellActivityQueryOK {
	if entries == nil {
		entries = []ActivityEntry{}
	}
	return ShellActivityQueryOK{T: TShellActivityQueryOK, ReqID: reqID, Entries: entries, Cursor: cursor}
}

type ShellActivityQueryErr struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
	Code  string `json:"code"`
	Msg   string `json:"msg,omitempty"`
}

func NewShellActivityQueryErr(reqID uint64, code, msg string) ShellActivityQueryErr {
	return ShellActivityQueryErr{T: TShellActivityQueryErr, ReqID: reqID, Code: code, Msg: msg}
}

type ShellActivityTail struct {
	T  string `json:"t"`
	On bool   `json:"on"`
}

func NewShellActivityTail(on bool) ShellActivityTail {
	return ShellActivityTail{T: TShellActivityTail, On: on}
}

type ShellActivityEntry struct {
	T     string        `json:"t"`
	Entry ActivityEntry `json:"entry"`
}

func NewShellActivityEntry(e ActivityEntry) ShellActivityEntry {
	return ShellActivityEntry{T: TShellActivityEntry, Entry: e}
}

type ShellActivityStats struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
}

func NewShellActivityStats(reqID uint64) ShellActivityStats {
	return ShellActivityStats{T: TShellActivityStats, ReqID: reqID}
}

type ShellActivityStatsOK struct {
	T     string        `json:"t"`
	ReqID uint64        `json:"req_id"`
	Stats ActivityStats `json:"stats"`
}

func NewShellActivityStatsOK(reqID uint64, st ActivityStats) ShellActivityStatsOK {
	return ShellActivityStatsOK{T: TShellActivityStatsOK, ReqID: reqID, Stats: st}
}

type ShellActivityClear struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
}

func NewShellActivityClear(reqID uint64) ShellActivityClear {
	return ShellActivityClear{T: TShellActivityClear, ReqID: reqID}
}

type ShellActivityClearOK struct {
	T     string `json:"t"`
	ReqID uint64 `json:"req_id"`
}

func NewShellActivityClearOK(reqID uint64) ShellActivityClearOK {
	return ShellActivityClearOK{T: TShellActivityClearOK, ReqID: reqID}
}
