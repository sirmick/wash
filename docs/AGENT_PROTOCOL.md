# The agentd protocol

com.wash.agentd (apps/agentd/be) owns every coding-agent session, the
approval queue, transcripts and workspaces. Frontends — the Agents manager
and each Agent window (com.wash.ai), wash-edit's agent tabs, the desktop
rail through the session gateway, hostgw for remote hosts — talk to it with
the messages below and nothing else.

The messages are defined once, as Go structs in `internal/agentproto`. The
TypeScript types (`web/lib/src/agent-protocol.gen.ts`, exported from
`@wash/ui` as the `agentproto` namespace) and the reference at the end of
this document are generated from them by `make gen-agent-protocol`, and
`make check-agent-protocol` (part of `make unit-test`, so CI) fails when
either is stale. A second frontend can be written from this document and the
generated TypeScript alone.

## Transport

Every message is a Wash app message (`EvtAppMsg`): a JSON object with a
`kind` and the payload's fields at the top level. Requests go to the app id
`com.wash.agentd`; agentd answers the sending instance directly, or pushes
to the instances that subscribed. The router attests the sender's app id and
instance id on every cross-app message.

Pushes travel in one of two queueing classes (docs/QOS.md §3):
`interactive`, or `bulk` for transcript streams and usage counters, which
must never sit ahead of typing or a permission question. Bulk frames can be
overtaken, so every session-scoped push carries the session's roster `key`,
and a frontend drops those for a session it is no longer showing.

## Versioning

`State.version` is `agentproto.Version`, and changes on any breaking change
to a message. A frontend compares it with the generated
`AGENT_PROTOCOL_VERSION` and refuses a version it does not know, with a
visible message (the Agent window's `ai-protocol-error`, the rail's
`rail-agent-protocol-error`). There is no negotiation and no older version
is kept: agentd and its frontends ship together.

## Adding or changing a message

1. Add or change the struct in `internal/agentproto` and its `register`
   call: kind, direction, who may send it, its reply, its class.
2. `make gen-agent-protocol`.
3. Handle it in agentd with `sdk.HandleFromVoid` on the struct, or send it
   with `agentproto.Send`, which picks the class from the registry.
4. Use the generated type in every frontend. Bump `Version` if an existing
   frontend would misread the new shape.

## Reference

Generated from `internal/agentproto` by `make gen-agent-protocol`; do not
edit by hand.

<!-- BEGIN GENERATED: make gen-agent-protocol -->

### Requests (to agentd)

| Kind | Payload | From | Reply / class | What it does |
|---|---|---|---|---|
| `agent_history` | [`AgentHistory`](#agenthistory) | any frontend | history | Search stored sessions. |
| `agent_resume` | [`AgentResume`](#agentresume) | any frontend |  | Reopen a stored session in an Agent window. |
| `agent_rename` | [`AgentRename`](#agentrename) | any frontend |  | Name a session, or clear the name. |
| `agent_delete` | [`AgentDelete`](#agentdelete) | any frontend | history_deleted | Delete a stored session. |
| `agent_prune` | [`AgentPrune`](#agentprune) | any frontend | history_pruned | Delete stored sessions older than an age. |
| `agent_set_key` | [`AgentSetKey`](#agentsetkey) | a manager (manager_subscribe) | key_saved | Store or clear a connection key. |
| `agent_test_key` | [`AgentTestKey`](#agenttestkey) | a manager (manager_subscribe) | key_test | Check a key with its provider. |
| `subscribe` | [`Subscribe`](#subscribe) | any app (the session gateway, hostgw) | state, now and on every change | Subscribe to the whole roster. |
| `unsubscribe` | [`Unsubscribe`](#unsubscribe) | a subscriber |  | Stop receiving state. |
| `agent_start` | [`AgentStart`](#agentstart) | a launcher (the Agents manager, an Agent window, wash-edit, wash ai --agent) | agent_started | Start a session from a stack tier, or an adapter on its defaults. |
| `agent_prompt` | [`AgentPrompt`](#agentprompt) | a frontend showing the session |  | Send a prompt, with attachments; queued while a turn runs. |
| `agent_cancel` | [`AgentCancel`](#agentcancel) | a frontend showing the session |  | Stop the running turn. |
| `agent_detach` | [`AgentDetach`](#agentdetach) | a frontend showing the session | detach, to the session's controller | Keep the session running with no window. |
| `agent_reattach` | [`AgentReattach`](#agentreattach) | any frontend |  | Open a window onto a running session. |
| `agent_stop` | [`AgentStop`](#agentstop) | any frontend |  | End a session and its adapter. |
| `agent_set_yolo` | [`AgentSetYolo`](#agentsetyolo) | a frontend showing the session |  | Turn host-side auto-approval on or off for the session. |
| `agent_set_mode` | [`AgentSetMode`](#agentsetmode) | a frontend showing the session |  | Switch the agent's approval preset. Refused for a reviewer. |
| `agent_set_config` | [`AgentSetConfig`](#agentsetconfig) | a frontend showing the session |  | Change an agent setting (model, effort, …). A reviewer's mode cannot change. |
| `agent_add_root` | [`AgentAddRoot`](#agentaddroot) | any frontend |  | Let the session reach another folder. |
| `agent_remove_root` | [`AgentRemoveRoot`](#agentremoveroot) | any frontend |  | Take a folder back. |
| `agent_answer` | [`AgentAnswer`](#agentanswer) | any frontend (answering anywhere resolves everywhere) |  | Answer a question, optionally remembering the rule. |
| `agent_default_prompt` | [`AgentDefaultPrompt`](#agentdefaultprompt) | any frontend | default_prompt | Read the stored default prompt. |
| `agent_set_default_prompt` | [`AgentSetDefaultPrompt`](#agentsetdefaultprompt) | any frontend | default_prompt | Store (or, with empty text, delete) the default prompt. |
| `transcript_subscribe` | [`TranscriptSubscribe`](#transcriptsubscribe) | any frontend showing the session | transcript_snapshot, when the watcher is new or asks for replay | Watch a session's transcript, or re-affirm the watch. |
| `workspace_refresh` | [`WorkspaceRefresh`](#workspacerefresh) | the session's controller | workspace_state | Resend the workspace frame for the session. |
| `workspace_action` | [`WorkspaceAction`](#workspaceaction) | the session's controller | workspace_result | A human's action in the workspace sidebar. |

### Pushes (from agentd)

| Kind | Payload | To | Reply / class | What it does |
|---|---|---|---|---|
| `history` | [`History`](#history) | the asker | interactive | Stored sessions matching a history query. |
| `history_deleted` | [`HistoryDeleted`](#historydeleted) | the asker | interactive | The outcome of a delete. |
| `history_pruned` | [`HistoryPruned`](#historypruned) | the asker | interactive | The outcome of a prune. |
| `key_saved` | [`KeySaved`](#keysaved) | the asker | interactive | The outcome of storing a key. |
| `key_test` | [`KeyTest`](#keytest) | the asker | interactive | What the provider said about a key. |
| `state` | [`RosterState`](#rosterstate) | every subscriber | interactive | The whole roster. Sent by the SDK StateService, which owns this message's encoding. |
| `manager_state` | [`ManagerState`](#managerstate) | every manager (manager_subscribe) | interactive | The manager's roster view, sent on subscribe and whenever it changes. |
| `session_state` | [`SessionState`](#sessionstate) | a session's controller | interactive, keyed | One session's row and questions, sent on claim and whenever they change. |
| `agent_started` | [`AgentStarted`](#agentstarted) | the starter | interactive | A started session's key, or the error that stopped it. |
| `detach` | [`Detach`](#detach) | the session's controller | interactive, keyed | The session was detached elsewhere; its window closes. |
| `default_prompt` | [`DefaultPrompt`](#defaultprompt) | the asker | interactive | The stored default prompt. |
| `transcript_snapshot` | [`TranscriptSnapshot`](#transcriptsnapshot) | the subscribing instance | bulk, keyed | A session's history, in bounded frames. |
| `transcript_event` | [`TranscriptEvent`](#transcriptevent) | every watcher of the session | bulk, keyed | One transcript event, new or changed; streamed text arrives as appended deltas. |

### Types

Each payload's fields, with their TypeScript type. `?` marks a field that
may be absent; `| null` one that may be null.

#### Adapter

Adapter is one way to reach an agent over ACP, as the launcher shows it: whether this box can start it, and if not, why.

| Field | Type | |
|---|---|---|
| `id` | `string` | ID is what the launcher and the roster call this agent. |
| `name` | `string` | Name is what a human reads. |
| `note?` | `string` | Note explains a greyed row: why this one cannot be used here. |
| `available` | `boolean` | Available is whether it can be launched here. |

#### AgentAddRoot

AgentAddRoot widens which folders a session may reach beyond its cwd.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `path` | `string` |  |

#### AgentAnswer

AgentAnswer answers a question (Ask) by its id.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `decision` | `string` | Decision is allow \| deny. |
| `remember` | `boolean` | Remember writes Rule (or the ask's suggested rule) to the policy, so the question is not asked again. |
| `rule` | `string` |  |
| `scope?` | `string` | Scope picks the table a remembered answer is written to: "" (or anything unrecognised) is the global one, "workspace" is the asking member's workspace's. An unknown value must not silently widen anything, so it falls back to the narrower, global behaviour. |

#### AgentCancel

AgentCancel ends the running turn; the agent answers with a cancelled stop.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### AgentDefaultPrompt

AgentDefaultPrompt asks for the stored default prompt's text.

No fields.

#### AgentDelete

AgentDelete deletes a stored session that is not running.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |

#### AgentDetach

AgentDetach leaves a session running with no window: its roster row stays and offers Reattach, and its controller window is told to close.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### AgentHistory

AgentHistory searches the stored sessions: their metadata and, with a query, their conversations.

| Field | Type | |
|---|---|---|
| `query?` | `string` |  |
| `limit?` | `number` | Limit bounds the answer; 0 and anything above 200 mean 200. |

#### AgentPrompt

AgentPrompt is another turn on a live session.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `text?` | `string` |  |
| `blocks?` | `PromptAttachment[]` | Blocks are attachments sent with the text: a pasted image, a file the composer's Attach button picked. Kept as a wash-shaped struct rather than acp.ContentBlock so the app→service wire is ours to validate — the router carries this from a window, and a window is not trusted to name a mime type or a path. |

#### AgentPrune

AgentPrune deletes stored sessions older than MaxAgeMS.

| Field | Type | |
|---|---|---|
| `max_age_ms` | `number` | MaxAgeMS is how old a session must be to go; 0 means every stored session that is not running. A duration rather than a cutoff so a browser clock on another machine cannot be the one deciding. |

#### AgentReattach

AgentReattach opens a window onto a running session.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### AgentRemoveRoot

AgentRemoveRoot narrows it again.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `path` | `string` |  |

#### AgentRename

AgentRename names a session, by roster key when it is running or by session id when it is not.

| Field | Type | |
|---|---|---|
| `key?` | `string` |  |
| `session_id?` | `string` |  |
| `title` | `string` |  |

#### AgentResume

AgentResume reopens a stored session: session/load replays it, and an Agent window opens on it (or the one already showing it comes forward).

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |

#### AgentSetConfig

AgentSetConfig changes one of the agent's own settings (Row.Configs): model, reasoning effort, plan mode, …

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `id` | `string` |  |
| `value` | `string` |  |

#### AgentSetDefaultPrompt

AgentSetDefaultPrompt stores the default prompt.

| Field | Type | |
|---|---|---|
| `text` | `string` |  |

#### AgentSetKey

AgentSetKey stores a connection key (State.Keys), or clears it with an empty value.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `value?` | `string` |  |

#### AgentSetMode

AgentSetMode switches the agent's approval preset (Row.Modes).

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `mode` | `string` |  |

#### AgentSetYolo

AgentSetYolo turns host-side auto-approval on or off for one session.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `on` | `boolean` |  |

#### AgentStart

AgentStart starts a session.

| Field | Type | |
|---|---|---|
| `stack?` | `string` | Stack and Tier choose the settings (stacks.go); Tier defaults to frontier. Agent and Model are the launcher's Advanced overrides, and Agent alone, with no stack, is how `wash ai --agent` starts. |
| `tier?` | `string` |  |
| `agent?` | `string` |  |
| `model?` | `string` |  |
| `cwd` | `string` | Cwd is the folder the session works in; empty is the home folder. |
| `prompt?` | `string` | Prompt is sent as the first turn, after the stored default prompt. |
| `open?` | `boolean` | Open asks agentd to open (or focus) an Agent window on the new session, for a starter that is not itself that window (the manager). |
| `req_id?` | `string` | ReqID is opaque to agentd and echoed back on agent_started, success or failure. A host with ONE session per process (wash-ai) never needs it — the reply can only be about the one thing it asked for. A host with several (wash-edit's agent tabs) cannot tell two concurrent starts apart without it, and a FAILED start carries no key at all, so there would be nothing to attribute the error to. |

#### AgentStarted

AgentStarted answers AgentStart: the new session's key, or why it failed.

| Field | Type | |
|---|---|---|
| `key?` | `string` |  |
| `session_id?` | `string` |  |
| `req_id?` | `string` |  |
| `error?` | `string` |  |

#### AgentStop

AgentStop ends a session and its adapter.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### AgentTestKey

AgentTestKey checks a key against its provider: the typed value, or the stored one when Value is empty.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `value?` | `string` |  |

#### Ask

Ask is one question waiting for a human.

| Field | Type | |
|---|---|---|
| `id` | `string` | ID is agentd's handle for this question; the answer names it. |
| `agent` | `string` | Agent / Tool / Subject are what the human reads: "claude wants to run `git push origin main`". |
| `tool` | `string` |  |
| `subject?` | `string` |  |
| `cwd?` | `string` |  |
| `dir?` | `string` |  |
| `suggested_rule?` | `string` | SuggestedRule is what "Always allow" would write. Shown ON the button — what you clicked is what gets saved. |
| `rule_cwd?` | `string` | RuleCwd is the directory that rule would be confined to, when it would be (agentpolicy.RuleScope): a Bash rule from one project must not buy the same command in every other checkout. |
| `row_key` | `string` | RowKey ties the question to its roster row (same "<instance>:<chan>" key for terminals; hosted sessions mint their own), so the sidebar can render it against the right agent. |
| `workspace_name?` | `string` | WorkspaceName is set when the asking session is a workspace member, and is what lets the prompt offer "always, for this workspace" alongside the global "always". The ID is deliberately NOT sent: the answer names a scope, never a target (see the answer path). |
| `age_ms` | `number` | AgeMS is how long it has been waiting, as of the push. |

#### Command

Command is one slash command the agent offers.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `description?` | `string` |  |

#### Config

Config is one agent setting the session can change.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `description?` | `string` |  |
| `current?` | `string` |  |
| `values?` | `ConfigValue[]` |  |

#### ConfigValue

| Field | Type | |
|---|---|---|
| `value` | `string` |  |
| `name` | `string` |  |
| `description?` | `string` |  |

#### DefaultPrompt

DefaultPrompt is the stored default prompt's text, answering either.

| Field | Type | |
|---|---|---|
| `text` | `string` |  |

#### Detach

Detach tells a session's controller window that the session was detached elsewhere (the rail, the manager), so it closes.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### Event

Event is one line in a transcript.

| Field | Type | |
|---|---|---|
| `seq` | `number` |  |
| `kind` | `string` | Kind is one of the Event* kinds above: message \| thought \| tool \| decision \| user \| terminal \| image \| collaboration. |
| `text?` | `string` | Text is the message body, accumulated across streamed chunks. |
| `tool_id?` | `string` | Tool fields, set when Kind == EventTool. |
| `tool_kind?` | `string` |  |
| `title?` | `string` |  |
| `status?` | `string` |  |
| `path?` | `string` | Path is the file a tool call touched (its first ACP location, or the diff's), so a host can open it. Diff is the unified diff of what the call changed, rendered once here from the agent's before/after pair (diff.go). Both on EventTool only. |
| `diff?` | `string` |  |
| `mime?` | `string` | Mime is set on EventImage; Text then holds the base64 bytes. |
| `reason?` | `string` | Reason and Detail are set on EventDecision. |
| `detail?` | `string` |  |
| `channel?` | `number` | Channel is set on EventTerminal: the raw channel id to render. |
| `at_ms` | `number` | AtMS is wall-clock at first append, for the FE's own clock anchoring. |
| `append?` | `boolean` | Append marks a wire-only delta: Text is what was ADDED to the event with this Seq since the last emit, not the whole message. Never set on a stored or snapshotted event (transcript_emit.go). |
| `text_len?` | `number` | TextLen is the message's byte length after this event applies, on message/thought events. A consumer applying a delta checks its own length + the delta against it, and asks for a replay on mismatch. |

#### History

History answers AgentHistory, newest first, each session stamped with what the roster says about it now.

| Field | Type | |
|---|---|---|
| `query` | `string` |  |
| `sessions` | `SessionMeta[] \| null` |  |

#### HistoryDeleted

HistoryDeleted answers AgentDelete.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |
| `error?` | `string` |  |

#### HistoryPruned

HistoryPruned answers AgentPrune: how many sessions went.

| Field | Type | |
|---|---|---|
| `deleted` | `number` |  |

#### KeySaved

KeySaved answers AgentSetKey.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `error?` | `string` |  |

#### KeyTest

KeyTest answers AgentTestKey with what the provider said.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `ok` | `boolean` |  |
| `detail` | `string` |  |

#### KeyView

KeyView is a key as the launcher shows it.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `set` | `boolean` |  |
| `hint?` | `string` | Hint is the stored key's last four characters. |
| `testable?` | `boolean` |  |

#### ManagerState

ManagerState is the Agents manager's view of the roster: every row with its transcript preview and workspace placement, without the per-session settings only a controller needs.

| Field | Type | |
|---|---|---|
| `state` | `State` |  |

#### Mode

Mode is one approval/sandbox preset an agent offers.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `description?` | `string` |  |

#### PromptAttachment

PromptAttachment is one attachment on its way to an ACP content block.

| Field | Type | |
|---|---|---|
| `type` | `string` |  |
| `mime?` | `string` | Image: base64 bytes and their mime type. |
| `data?` | `string` |  |
| `path?` | `string` | File: an absolute path, confined against the session cwd before it becomes a resource_link. |
| `name?` | `string` |  |

#### RosterState

RosterState is the whole roster, sent by the StateService to every subscriber on each change.

| Field | Type | |
|---|---|---|
| `state` | `State` |  |

#### Row

Row is one hosted agent session.

| Field | Type | |
|---|---|---|
| `key` | `string` | Key identifies the session on the roster: "acp:<n>", minted by agentd. Every session-scoped message names it. |
| `agent` | `string` | Agent is the slug ("claude", "codex", …). |
| `state` | `string` | State is running \| working \| needs-input \| done \| failed, or "stale" once an exited session's row is ageing out. |
| `reason?` | `string` | Reason qualifies the state: which input is wanted (permission \| idle), or how a turn or session ended (cancelled, error, exited, …). |
| `session_id?` | `string` | SessionID is the agent's own session id — what makes a row actionable later (`--resume`, copy-session-id). |
| `cwd?` | `string` | Cwd is where the agent is working; Dir is its basename, which is what a 300px sidebar column can actually show. |
| `dir?` | `string` |  |
| `branch?` | `string` | Branch / Dirty come from agentd shelling git in Cwd, lazily and cached — never from the agent's hooks (§7). |
| `dirty?` | `boolean` |  |
| `since_ms` | `number` | SinceMS is how long the row has been in this state, as of the push. The FE anchors its own clock to it (no cross-clock comparison). |
| `used?` | `number` | Used / Size are the agent's context accounting (usage_update), and Title is its own name for the session (session_info_update). |
| `size?` | `number` |  |
| `title?` | `string` |  |
| `preview?` | `string` | Preview is populated only in the compact manager projection. Live transcript changes arrive as bounded Bulk patches, never by republishing the complete roster. |
| `mode?` | `string` | Mode is the agent's active approval preset and Modes what it offers (docs/AGENT_APP.md §9). Empty for an agent with no such notion. |
| `modes?` | `Mode[]` |  |
| `yolo?` | `boolean` | Yolo is wash answering this session's permission questions with "allow" instead of asking. Host-side and per-session; it is on the row so every surface that shows the session can SAY so — an auto-approving agent that looks like any other is the failure mode this flag exists to prevent. |
| `configs?` | `Config[]` | Configs is the agent's generic settings block (model, reasoning effort, plan mode…). Commands are its own slash commands. |
| `commands?` | `Command[]` |  |
| `detached?` | `boolean` | Detached marks a session still running with no window pointing at it — the sidebar offers Reattach rather than focus. |
| `queued?` | `number` | Queued is how many prompts are waiting for the current turn to end (docs/AGENT_MESSENGER.md semantics: a message typed mid-reply is sent when the reply finishes, not dropped and not interleaved). |
| `roots?` | `string[]` | Roots are folders this session may reach BEYOND its cwd (roots.go). On the row because every surface that shows a session must be able to say how wide it is: a session with three extra roots is a different thing from one confined to its own folder, and only the person who widened it would otherwise know. |
| `stale?` | `boolean` | Stale marks the row of a session that is no longer hosted: shown greyed, then dropped. |
| `workspace?` | `RowWorkspace` | Workspace places the session in a workspace team, so the Agents window lists members under the orchestrator that leads them. Set only on the manager's projection. |

#### RowWorkspace

RowWorkspace is one session's place in a workspace team.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `lead_session` | `string` | LeadSession is the orchestrator's session: a member row nests under the row with this session_id. |
| `orchestrator?` | `boolean` |  |
| `member` | `string` |  |
| `role?` | `string` |  |
| `package?` | `string` |  |
| `package_title?` | `string` |  |

#### Session

Session is one remembered agent session.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |
| `agent` | `string` |  |
| `connection?` | `string` | Connection is what a resume must launch through again; Stack and Tier are what the launcher defaults to next time (launchRecord). |
| `stack?` | `string` |  |
| `tier?` | `string` |  |
| `cwd?` | `string` |  |
| `dir?` | `string` |  |
| `title?` | `string` | Title is what this session was ABOUT, in the agent's own words — it names its sessions on session_info_update once it works out what the work is. "codex · mick" tells you nothing a week later; "Fix the reconnect banner race" does. |
| `user_title?` | `string` | UserTitle is the name a PERSON gave the session (session_admin.go). When set it is what publishHistory puts in Title; the agent's own title stays here underneath so clearing the user's falls back to it. |
| `last_seen` | `number` | LastSeen is unix seconds — an absolute the FE renders as "2h ago", and the only field a keepalive touches. |
| `live?` | `boolean` | Live is set on the way out to the FE: a session whose agent is running right now is in the roster above, so the Recent list greys it rather than offering to resume what is already here. |
| `detached?` | `boolean` | Detached is a live session with no window pointing at it. Live and REACHABLE are not the same thing, and treating them as one is what made the History menu useless in exactly the case you open it for. agent_detach sets the flag and closes the window but never retires the row, so a detached session is still "live" — and the menu, which hides live sessions to avoid offering to duplicate a running one, hid the one thing you were trying to get back. A detached session is not something to resume. It is something to reattach to, which is a different verb with a different outcome. |
| `row_key?` | `string` | RowKey is the roster key this session is running as, present only while it has a row. Reattach is key-addressed, not session-id addressed, so the menu needs this to offer the verb at all. |

#### SessionMeta

SessionMeta is what the history panel lists.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |
| `agent?` | `string` |  |
| `connection?` | `string` |  |
| `stack?` | `string` |  |
| `tier?` | `string` |  |
| `model?` | `string` |  |
| `cwd?` | `string` |  |
| `dir?` | `string` |  |
| `title?` | `string` |  |
| `user_title?` | `string` | UserTitle is the person's name for the session, when they gave one. Title above is then the SAME string — the effective title, so every reader shows the name without knowing where it came from — and this field says it was theirs. |
| `started_ms?` | `number` |  |
| `ended_ms?` | `number` |  |
| `end_reason?` | `string` |  |
| `events?` | `number` |  |
| `bytes?` | `number` | Bytes is the transcript's size on disk, so the UI can say what history costs and offer to prune the expensive ones. |
| `preview?` | `string` | Preview is a few recent human/agent lines taken from the same bounded tail read used for the metadata. It gives the always-visible history list enough context without loading or sending whole transcripts. |
| `snippet?` | `string` | Snippet is the line that matched, with a little either side. Absent when the query matched metadata instead (the row already shows the title and directory, so quoting them back is noise) or when there was no query at all. |
| `live?` | `boolean` | Live / Detached / RowKey are stamped on the way out from the roster, not read from the file — the transcript index knows what a session WAS, and only the roster knows what it is doing now. The panel did not have these at all, so it would happily offer to resume a session that was already running: the precise duplication the menu's filter exists to prevent, in the view that had no filter. Same predicate, both views (see rosterIndex). |
| `detached?` | `boolean` |  |
| `row_key?` | `string` |  |

#### SessionState

SessionState is one Agent window's view: its own row and the questions waiting on it.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `state` | `State` |  |

#### StackView

StackView is a stack as the launcher shows it.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `available` | `boolean` | Available is every tier startable here; Note says why not. |
| `note?` | `string` |  |
| `tiers?` | `TierView[]` |  |

#### State

State is the public roster.

| Field | Type | |
|---|---|---|
| `version` | `number` | Version is always the package's Version (see above). |
| `rows` | `Row[] \| null` |  |
| `asks?` | `Ask[]` | Asks are permission questions waiting for a human (§12). They ride the roster's own push so the sidebar needs no second subscription. |
| `recent?` | `Session[]` | Recent is the remembered session history (§13) — what a reboot or a closed window would otherwise have cost you. |
| `adapters?` | `Adapter[]` | Adapters is which agents this box can actually launch over ACP (docs/AGENT_APP.md §6). The launcher renders unavailable ones greyed with their reason rather than hiding them, so "why can I not pick Claude here" has an answer on screen. |
| `stacks?` | `StackView[]` | Stacks are what the launcher offers first (stacks.go): each with the availability of its tiers, greyed with a reason like an adapter. |
| `keys?` | `KeyView[]` | Keys are the connection keys the launcher can store: set or not, and a stored key's last four characters. Never a value. |
| `has_default_prompt?` | `boolean` | HasDefaultPrompt says whether a stored default prompt exists, so the launcher can say that a new session will not start empty. Only the FLAG rides the state push — the text itself is fetched on demand (agent_default prompt), because a page of prose on every roster push would reach every subscriber several times a second during a turn. |

#### Subscribe

Subscribe asks for the whole roster, now and on every change.

No fields.

#### TierView

TierView is one tier as the launcher shows it.

| Field | Type | |
|---|---|---|
| `tier` | `string` |  |
| `adapter` | `string` |  |
| `connection?` | `string` |  |
| `model?` | `string` |  |
| `thinking?` | `string` |  |
| `capability?` | `string` |  |
| `read_only?` | `string` | ReadOnly is set on the review tier: "enforced" where the adapter's tools are restricted (Claude Code's reviewer capability), otherwise "instruction" — the reviewer is asked not to write, and could. |
| `available` | `boolean` |  |
| `note?` | `string` |  |

#### TranscriptEvent

TranscriptEvent is one new or changed event.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `event` | `Event` |  |

#### TranscriptSnapshot

TranscriptSnapshot is a session's history, sent in bounded frames: the first has Reset (replace what you hold), the rest append.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `reset` | `boolean` |  |
| `events` | `Event[] \| null` |  |

#### TranscriptSubscribe

TranscriptSubscribe watches a session's transcript, and re-affirms the watch: a watcher agentd has not heard from for its TTL is dropped, so a frontend repeats this every agentclient.WatcherRefresh.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `replay?` | `boolean` | Replay asks for the whole history again even from a watcher agentd already knows, after a frontend lost its copy (a reload, a missed delta). |

#### Unsubscribe

Unsubscribe stops a Subscribe.

No fields.

#### WorkspaceAction

WorkspaceAction is a human's action in the workspace sidebar.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `name` | `string` | Name is the operation: decision_response \| member_open \| member_resume \| member_message \| member_inspect. |
| `arguments` | `unknown` |  |

#### WorkspaceRefresh

WorkspaceRefresh asks for the session's workspace frame again.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

<!-- END GENERATED -->
