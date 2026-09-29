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

## Trust and roles

The router attests an app's id and instance id and nothing else, and agentd
does not decide what a frontend may do by which app it is. It decides by
what the frontend has claimed:

- **Manager**: an instance that sent `manager_subscribe`. It receives the
  manager's roster view and may set and test connection keys
  (`agent_set_key`, `agent_test_key`).
- **Controller**: the instance holding a session's lease, taken with
  `session_claim` or with `agent_start {claim: true}`, or given by agentd to
  a window it opened (`attach`). At most one instance holds a session's
  lease; a second claim is refused (`claim_denied`) and the holder raised.
  Focus goes to the controller, a detach closes it, and only it may make
  the workspace sidebar's requests (`workspace_refresh`, `workspace_action`).

Everything else — prompting, cancelling, answering questions, stopping —
is open to any frontend: those are the person's actions, and any window
showing a session may take them. A role is a claim, not a credential: every
frontend is an app of the same user on the same router, and what the roles
enforce is that two windows do not both behave as a session's own.

## Desktop events

Some of what agentd does is not a message to a frontend but a request of
the desktop: open a window on a session (`open_session`), and bring
something to the person's attention (`notify`: a question waiting, a
workspace flash, a failure). These are typed like the messages, listed in
the reference below, and all go through one handler in agentd
(`apps/agentd/be/desktop.go`). Wash's implementation spawns an Agent window
and hands it the session with `attach`, and posts notifications through the
router, which shows a question even when no Agent window is open. Nothing
else in agentd spawns a window or posts a notification.

The Agent window's own shortcuts (open a terminal, file manager or editor in
the session's folder, open a file a tool touched) are that window's business,
handled by its own backend with the window's confinement, and are not part
of this protocol.

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
| `agent_set_catalog` | [`AgentSetCatalog`](#agentsetcatalog) | a manager (manager_subscribe) | catalog_saved | Store a catalog, whole, in agents.json. |
| `agent_delete_catalog` | [`AgentDeleteCatalog`](#agentdeletecatalog) | a manager (manager_subscribe) | catalog_saved | Remove a catalog from agents.json: a built-in reverts, the user's own is deleted. |
| `agent_set_launch` | [`AgentSetLaunch`](#agentsetlaunch) | a manager (manager_subscribe) |  | Store the launcher's remembered permission default. |
| `manager_subscribe` | [`ManagerSubscribe`](#managersubscribe) | any frontend, which becomes a manager | manager_state, now and on every change | Subscribe to the manager's roster view. |
| `session_claim` | [`SessionClaim`](#sessionclaim) | any frontend | session_claimed then session_state, or claim_denied | Take the controller lease on a session. |
| `wash.focus` | [`Focus`](#focus) | the shell (a notification click, no sender) or any frontend |  | Bring a session's window forward, opening one if needed. |
| `agent_history` | [`AgentHistory`](#agenthistory) | any frontend | history | Search stored sessions. |
| `agent_resume` | [`AgentResume`](#agentresume) | any frontend |  | Reopen a stored session in an Agent window. |
| `agent_rename` | [`AgentRename`](#agentrename) | any frontend |  | Name a session, or clear the name. |
| `agent_delete` | [`AgentDelete`](#agentdelete) | any frontend | history_deleted | Delete a stored session. |
| `agent_prune` | [`AgentPrune`](#agentprune) | any frontend | history_pruned | Delete stored sessions older than an age. |
| `agent_set_key` | [`AgentSetKey`](#agentsetkey) | a manager (manager_subscribe) | key_saved | Store or clear a connection key. |
| `agent_test_key` | [`AgentTestKey`](#agenttestkey) | a manager (manager_subscribe) | key_test | Check a key with its provider. |
| `subscribe` | [`Subscribe`](#subscribe) | any app (the session gateway, hostgw) | state, now and on every change | Subscribe to the whole roster. |
| `unsubscribe` | [`Unsubscribe`](#unsubscribe) | a subscriber |  | Stop receiving state. |
| `agent_start` | [`AgentStart`](#agentstart) | a launcher (the Agents manager, an Agent window, wash-edit, wash ai --agent) | agent_started | Start a session from a catalog and model, or an adapter on its defaults. |
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
| `agent_question_answer` | [`AgentQuestionAnswer`](#agentquestionanswer) | any frontend (answering anywhere resolves everywhere) |  | The human's answers to a question set, or a decline. |
| `agent_answer` | [`AgentAnswer`](#agentanswer) | any frontend (answering anywhere resolves everywhere) |  | Answer a question, optionally remembering the rule. |
| `agent_default_prompt` | [`AgentDefaultPrompt`](#agentdefaultprompt) | any frontend | default_prompt | Read the stored default prompt. |
| `agent_set_default_prompt` | [`AgentSetDefaultPrompt`](#agentsetdefaultprompt) | any frontend | default_prompt | Store (or, with empty text, delete) the default prompt. |
| `transcript_subscribe` | [`TranscriptSubscribe`](#transcriptsubscribe) | any frontend showing the session | transcript_snapshot, when the watcher is new or asks for replay | Watch a session's transcript, or re-affirm the watch. |
| `workspace_refresh` | [`WorkspaceRefresh`](#workspacerefresh) | the session's controller | workspace_state | Resend the workspace frame for the session. |
| `workspace_action` | [`WorkspaceAction`](#workspaceaction) | the session's controller | workspace_result | A human's action in the workspace sidebar. |

### Pushes (from agentd)

| Kind | Payload | To | Reply / class | What it does |
|---|---|---|---|---|
| `catalog_saved` | [`CatalogSaved`](#catalogsaved) | the asker | interactive | The outcome of storing or removing a catalog. |
| `session_claimed` | [`SessionClaimed`](#sessionclaimed) | the claimant | interactive, keyed | The lease is yours. |
| `claim_denied` | [`ClaimDenied`](#claimdenied) | the claimant | interactive, keyed | Another window holds the lease. |
| `wash.focus` | [`Raise`](#raise) | the session's controller | interactive, keyed | Come to the front. |
| `attach` | [`Attach`](#attach) | a window agentd opened for a session | interactive, keyed | The session this new window shows. |
| `history` | [`History`](#history) | the asker | interactive | Stored sessions matching a history query. |
| `history_deleted` | [`HistoryDeleted`](#historydeleted) | the asker | interactive | The outcome of a delete. |
| `history_pruned` | [`HistoryPruned`](#historypruned) | the asker | interactive | The outcome of a prune. |
| `key_saved` | [`KeySaved`](#keysaved) | the asker | interactive | The outcome of storing a key. |
| `key_test` | [`KeyTest`](#keytest) | the asker | interactive | What the provider said about a key. |
| `usage_patch` | [`UsagePatch`](#usagepatch) | roster subscribers, managers, and each row's controller | bulk | New context counters for some rows. |
| `preview_patch` | [`PreviewPatch`](#previewpatch) | managers | bulk | New transcript previews for some rows. |
| `state` | [`RosterState`](#rosterstate) | every subscriber | interactive | The whole roster. Sent by the SDK StateService, which owns this message's encoding. |
| `manager_state` | [`ManagerState`](#managerstate) | every manager (manager_subscribe) | interactive | The manager's roster view, sent on subscribe and whenever it changes. |
| `session_state` | [`SessionState`](#sessionstate) | a session's controller | interactive, keyed | One session's row and questions, sent on claim and whenever they change. |
| `agent_started` | [`AgentStarted`](#agentstarted) | the starter | interactive | A started session's key, or the error that stopped it. |
| `detach` | [`Detach`](#detach) | the session's controller | interactive, keyed | The session was detached elsewhere; its window closes. |
| `default_prompt` | [`DefaultPrompt`](#defaultprompt) | the asker | interactive | The stored default prompt. |
| `transcript_snapshot` | [`TranscriptSnapshot`](#transcriptsnapshot) | the subscribing instance | bulk, keyed | A session's history, in bounded frames. |
| `transcript_event` | [`TranscriptEvent`](#transcriptevent) | every watcher of the session | bulk, keyed | One transcript event, new or changed; streamed text arrives as appended deltas. |
| `workspace_state` | [`WorkspaceState`](#workspacestate) | the session's controller | bulk, keyed | The whole workspace sidebar frame. |
| `workspace_patch` | [`WorkspacePatch`](#workspacepatch) | the session's controller | bulk, keyed | What changed in the frame since the frame with sequence base. |
| `workspace_result` | [`WorkspaceResult`](#workspaceresult) | the acting controller | interactive, keyed | The outcome of a workspace_action. |

### Desktop events (agentd to the desktop)

| Kind | Payload | Handled by | Reply / class | What it does |
|---|---|---|---|---|
| `open_session` | [`OpenSession`](#opensession) | the desktop | interactive | Open a window on a session; it is attached once it starts. |
| `notify` | [`Notify`](#notify) | the desktop | interactive | A notification: a question waiting, a flash message, a failure to report. |

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

#### AdapterOptions

AdapterOptions is what one adapter offered the last time a session of it started here (agentd remembers it across restarts).

| Field | Type | |
|---|---|---|
| `adapter` | `string` | Adapter is the adapter id ("claude", "codex", …). |
| `version?` | `string` | Version is the adapter's own version string, as it introduced itself. |
| `modes?` | `Mode[]` | Modes are its approval presets; Configs its settings, of which the ones in the "model" and "thought_level" categories are the model and effort lists. Current values are those of the session that reported them and mean nothing here. |
| `configs?` | `Config[]` |  |

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

#### AgentDeleteCatalog

AgentDeleteCatalog removes a catalog from agents.json: a built-in goes back to what wash ships, the user's own is gone.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |

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
| `all?` | `boolean` | All includes the sessions a workspace launched (its members). Without it the answer is top-level sessions only: one orchestrator can launch dozens of members, and they buried the conversations people started. |

#### AgentProfile

AgentProfile describes launch settings and an optional enforced capability profile.

| Field | Type | |
|---|---|---|
| `capability?` | `string` |  |
| `approval?` | `string` | Approval "auto" launches the member with host auto-approval on; "" and "ask" leave every unmatched tool call to the human. |
| `provider` | `string` |  |
| `connection?` | `string` | Connection names how the provider is reached ("opencode@openrouter"); empty is the provider direct. agentd owns the list and checks it. |
| `model?` | `string` |  |
| `effort?` | `string` |  |
| `configs?` | `Record<string, string>` |  |
| `subagents?` | `string` | Subagents "deny" removes the provider's own subagent tool, so the member's work stays in its transcript and the workspace's accounting. "" and "allow" leave it available. |

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

#### AgentQuestionAnswer

AgentQuestionAnswer answers a Question by its id: accept with the answers (question id to answer; a question left out is skipped), or decline.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `action` | `string` | Action is accept \| decline. |
| `answers?` | `Record<string, QuestionAnswer>` |  |

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

#### AgentSetCatalog

AgentSetCatalog stores one catalog under agents.json `catalogs`, whole, as the Catalog tab holds it.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `catalog` | `CatalogSpec` |  |

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

#### AgentSetLaunch

AgentSetLaunch stores the launcher's remembered permission default (State.Launch), whole.

| Field | Type | |
|---|---|---|
| `launch` | `LaunchPrefs` |  |

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
| `catalog?` | `string` | Catalog and Model choose the settings (catalogs.go): Model is a slot of a curated catalog (frontier, coding, small; frontier when empty) or a model id the catalog's adapter offers (empty is its default on an auto catalog). Agent alone, with no catalog, is how `wash ai --agent` starts: that adapter on its defaults. |
| `model?` | `string` |  |
| `agent?` | `string` |  |
| `configs?` | `Record<string, string>` | Configs are the launcher's Advanced settings, by the adapter's own option ids (effort, fast mode, …), applied over the catalog's. |
| `mode?` | `string` | Mode is the adapter session mode to start in (State.Launch's default unless the launcher's Permissions row was changed for this start); empty is the adapter's default. Yolo starts with host-side auto-approval on. |
| `yolo?` | `boolean` |  |
| `cwd` | `string` | Cwd is the folder the session works in; empty is the home folder. |
| `prompt?` | `string` | Prompt is sent as the first turn, after the stored default prompt. |
| `open?` | `boolean` | Open asks agentd to open (or focus) an Agent window on the new session, for a starter that is not itself that window (the manager). |
| `claim?` | `boolean` | Claim makes the starter the session's controller (see Roles): an Agent window starting the session it will show. |
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

#### Assignment

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `assigner` | `string` |  |
| `member_id` | `string` |  |
| `node?` | `string` | Node is the plan node the work is on. |
| `text` | `string` |  |
| `state` | `string` |  |
| `result?` | `string` |  |

#### Attach

Attach hands a window agentd opened the session it is to show; the lease is already its.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### CatalogSaved

CatalogSaved answers AgentSetCatalog and AgentDeleteCatalog.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `error?` | `string` |  |

#### CatalogSpec

CatalogSpec is a catalog as written: a name and either an adapter (an auto catalog, listing what that adapter offers) or three slots.

| Field | Type | |
|---|---|---|
| `name` | `string` |  |
| `adapter?` | `string` |  |
| `connection?` | `string` |  |
| `slots?` | `Record<string, SlotSpec>` | Slots is keyed by slot name (frontier, coding, small); all three are required for a curated catalog, and none is given for an auto one. |

#### CatalogView

CatalogView is a catalog as the launcher and the Catalog tab show it.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `adapter?` | `string` | Adapter and Connection are an auto catalog's: the adapter whose models it lists, reached direct or through the connection. |
| `connection?` | `string` |  |
| `available` | `boolean` | Available is every slot (or the adapter) startable here; Note says why not. |
| `note?` | `string` |  |
| `slots?` | `SlotView[]` |  |
| `builtin?` | `boolean` | Builtin is a catalog wash ships (catalogs.json); Overridden says agents.json changes it. A catalog that is neither is the user's own. The Catalog tab offers "reset" for an overridden built-in and "delete" for the user's own. |
| `overridden?` | `boolean` |  |

#### ClaimDenied

ClaimDenied refuses the lease: another instance holds it, and has been raised.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

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
| `category?` | `string` | Category is the ACP category ("model", "thought_level", …), which is how a setting is matched across adapters that name it differently. |
| `current?` | `string` |  |
| `values?` | `ConfigValue[]` |  |

#### ConfigValue

| Field | Type | |
|---|---|---|
| `value` | `string` |  |
| `name` | `string` |  |
| `description?` | `string` |  |

#### ConnectionView

ConnectionView is one named connection: which adapter it launches, and the key it needs, if any.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `adapter` | `string` |  |
| `key?` | `string` |  |

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

#### Focus

Focus asks for a session's window to come forward, opening one if nothing is showing it.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

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

#### LaunchPrefs

LaunchPrefs is the remembered default for the two permission settings a launch has: the adapter's own approval preset and wash's auto-approval.

| Field | Type | |
|---|---|---|
| `mode?` | `Record<string, string>` | Mode is the adapter's session mode to start in, by adapter id: the names are the adapter's own ("acceptEdits" on Claude Code, "read-only" on Codex), so one remembered value cannot serve two adapters. |
| `yolo?` | `boolean` | Yolo starts every session with host-side auto-approval on. |

#### ManagerState

ManagerState is the Agents manager's view of the roster: every row with its transcript preview and workspace placement, without the per-session settings only a controller needs.

| Field | Type | |
|---|---|---|
| `state` | `State` |  |

#### ManagerSubscribe

ManagerSubscribe registers the sender as a manager and asks for the manager's roster view, now and on every change.

No fields.

#### Member

| Field | Type | |
|---|---|---|
| `key?` | `string` |  |
| `node?` | `string` | Node is the plan node the member works on; empty is the team (the orchestrator, an Architect). |
| `role?` | `string` |  |
| `instructions?` | `string` |  |
| `initial_task?` | `string` |  |
| `handoff?` | `string` | Handoff is the handoff a member launched with handoff_from reads in its first message: what the member it replaces had done and knew. |
| `usage?` | `Usage` |  |
| `catalog?` | `string` | Catalog and Model are what the member was asked to run on: the catalog (the workspace's unless the member named one) and the model as given, a slot name or an id. LaunchSettings is what that resolved to, with the member's own settings on top, fixed when its key was reserved: a later catalog change moves no running member. |
| `model?` | `string` |  |
| `launch_settings?` | `AgentProfile` |  |
| `initial_configs?` | `Record<string, string>` |  |
| `adjusted_configs?` | `Record<string, string>` | Adjusted are settings the orchestrator changed on the live member (member_control configure), applied over LaunchSettings on every resume. Kept apart so the keyed launch definition stays as declared. |
| `auto_approve?` | `boolean` | AutoApprove is whether host auto-approval is on for this member now: set from Approval at launch, and by the human's toggle afterwards. Kept here, not on the session, so a restart (which pauses rather than ends a member) does not silently switch it off; it ends with the workspace, never outliving the job it was granted for. |
| `id` | `string` |  |
| `name` | `string` |  |
| `provider` | `string` |  |
| `cwd` | `string` |  |
| `session_id` | `string` |  |
| `creator` | `string` |  |
| `lifetime` | `string` |  |
| `state` | `string` |  |
| `status?` | `string` |  |
| `emoji?` | `string` |  |
| `waiting?` | `string` |  |
| `waiting_for?` | `string` |  |
| `waiting_on?` | `string[]` | WaitingOn is a set of assignments this member created and is waiting for as a whole: their results are held and delivered together in one turn once every one has completed or failed. One wake-up per review round instead of one per reviewer. |
| `status_updated_at?` | `number` |  |
| `can_spawn` | `boolean` |  |
| `retire?` | `boolean` |  |

#### Message

| Field | Type | |
|---|---|---|
| `thread_id?` | `string` |  |
| `id` | `string` |  |
| `swarm_id` | `string` |  |
| `sender` | `string` |  |
| `recipient` | `string` |  |
| `type` | `string` |  |
| `body` | `string` |  |
| `reply_to?` | `string` |  |
| `assignment_id?` | `string` |  |
| `request_id?` | `string` |  |
| `delivery` | `string` |  |
| `created_at` | `number` |  |
| `task?` | `boolean` | Task marks the instruction Assign wrote to hand its assignment over. Only that one goes stale when the assignment resolves: a later instruction naming the same assignment is a new ask about finished work, not a duplicate of this one. |
| `questions?` | `QuestionSet` | Questions is a decision_request's question set; Answers the owner's answers on its decision_response. |
| `answers?` | `Record<string, QuestionAnswer>` |  |

#### Mode

Mode is one approval/sandbox preset an agent offers.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `name` | `string` |  |
| `description?` | `string` |  |

#### Node

Node is one plan node.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `title` | `string` |  |
| `emoji?` | `string` |  |
| `template?` | `string` | Template is how the node is drawn: milestone, package, step or note. Wash gives it no other meaning, except that a milestone with no children is a sketch. |
| `parent?` | `string` | Parent is the node this one sits inside. |
| `needs?` | `string[]` | Needs are node IDs that must be done, or QA thread IDs that must be resolved, before work on this node (or anything inside it) starts. |
| `body?` | `string` |  |
| `state` | `string` | State is todo, active, reported, done, failed, or a short word of the orchestrator's own. |
| `revision` | `number` |  |
| `overrides?` | `string[]` | Overrides record each start with needs unmet, and why. |

#### Notify

Notify brings something to the person's attention.

| Field | Type | |
|---|---|---|
| `key?` | `string` |  |
| `title` | `string` |  |
| `body?` | `string` |  |
| `level` | `string` | Level is info \| warn \| error. |

#### OpenSession

OpenSession opens a window showing a session.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### Option

Option is one choice.

| Field | Type | |
|---|---|---|
| `label` | `string` |  |
| `description?` | `string` |  |

#### PendingQuestion

PendingQuestion is a question set waiting for the human, shown pinned above the asking session's composer (and in its workspace tab) until answered.

| Field | Type | |
|---|---|---|
| `id` | `string` | ID is agentd's handle; the answer names it. A workspace decision's is its message id. |
| `row_key` | `string` | RowKey is the asking session's roster row. |
| `agent?` | `string` |  |
| `source` | `string` | Source is elicitation (the adapter asked) or decision (a workspace member's decision_request). |
| `set` | `QuestionSet` |  |
| `workspace_name?` | `string` | WorkspaceName and MemberID place a workspace member's question. |
| `member_id?` | `string` |  |
| `age_ms` | `number` | AgeMS is how long it has been waiting, as of the push. |

#### PreviewPatch

PreviewPatch updates rows' transcript previews (Row.Preview) in the manager's view.

| Field | Type | |
|---|---|---|
| `rows` | `PreviewRow[] \| null` |  |

#### PreviewRow

PreviewRow is one row's preview.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `preview?` | `string` |  |

#### PromptAttachment

PromptAttachment is one attachment on its way to an ACP content block.

| Field | Type | |
|---|---|---|
| `type` | `string` |  |
| `mime?` | `string` | Image: base64 bytes and their mime type. |
| `data?` | `string` |  |
| `path?` | `string` | File: an absolute path, confined against the session cwd before it becomes a resource_link. |
| `name?` | `string` |  |

#### QADocumentStatus

QADocumentStatus is where a file (or directory) Wash writes for the workspace stands on disk: the QA thread files, the plan file.

| Field | Type | |
|---|---|---|
| `path` | `string` |  |
| `state` | `string` | State is unconfigured \| pending \| saved \| error. |
| `error?` | `string` |  |
| `updated_at?` | `number` |  |

#### QAEvent

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `author` | `string` |  |
| `kind` | `string` |  |
| `body` | `string` |  |
| `message_id?` | `string` |  |
| `created_at` | `number` |  |

#### QAThread

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `node` | `string` | Node is the plan node the thread is about. |
| `title` | `string` |  |
| `creator` | `string` |  |
| `assignee` | `string` |  |
| `state` | `string` |  |
| `blocking` | `boolean` |  |
| `revision` | `number` |  |
| `decision_refs` | `string[] \| null` |  |
| `evidence?` | `string` |  |
| `resumed?` | `boolean` | Resumed marks a thread resolved in an earlier workspace and read back from its QA file: its evidence is about that workspace's code. |
| `archived?` | `boolean` | Archived marks a resolved thread read back as a header only: its events stay in its file until something needs them. |
| `events` | `QAEvent[] \| null` |  |

#### Question

Question is one question in a set.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `header?` | `string` | Header is a short label for the question (a chip, a tab). |
| `question` | `string` |  |
| `options?` | `Option[]` |  |
| `multi?` | `boolean` | Multi lets the owner pick several options. |
| `recommended?` | `string` | Recommended is the label of the option the asker recommends. |
| `no_text?` | `boolean` | NoText says only the options are answers (a form field with no free text); a decision_request question always takes the owner's words. |

#### QuestionAnswer

QuestionAnswer is the owner's answer to one question: the options picked, their own words, or neither (skipped).

| Field | Type | |
|---|---|---|
| `selected?` | `string[]` |  |
| `text?` | `string` |  |

#### QuestionSet

QuestionSet is one ask of the owner.

| Field | Type | |
|---|---|---|
| `title?` | `string` |  |
| `questions` | `Question[] \| null` |  |

#### Raise

Raise tells a session's controller to come forward.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

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
| `background?` | `string` | Background is the work the session left running in the background (a Bash run in the background): what it is, while it runs. |
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
| `node?` | `string` | Node and NodeTitle are the plan node the member works on. |
| `node_title?` | `string` |  |

#### Rule

Rule is one line of the table.

| Field | Type | |
|---|---|---|
| `match` | `string` | Match is `Tool` or `Tool(pattern)`. |
| `decision` | `string` | Decision is allow \| deny \| ask. |
| `cwd?` | `string` | Cwd scopes the rule to requests at or under this directory. |

#### Session

Session is one remembered agent session.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |
| `agent` | `string` |  |
| `connection?` | `string` | Connection is what a resume must launch through again; Catalog and Model are what the launcher defaults to next time (launchRecord). |
| `catalog?` | `string` |  |
| `model?` | `string` |  |
| `cwd?` | `string` |  |
| `dir?` | `string` |  |
| `title?` | `string` | Title is what this session was ABOUT, in the agent's own words — it names its sessions on session_info_update once it works out what the work is. "codex · mick" tells you nothing a week later; "Fix the reconnect banner race" does. |
| `user_title?` | `string` | UserTitle is the name a PERSON gave the session (session_admin.go). When set it is what publishHistory puts in Title; the agent's own title stays here underneath so clearing the user's falls back to it. |
| `last_seen` | `number` | LastSeen is unix seconds — an absolute the FE renders as "2h ago", and the only field a keepalive touches. |
| `live?` | `boolean` | Live is set on the way out to the FE: a session whose agent is running right now is in the roster above, so the Recent list greys it rather than offering to resume what is already here. |
| `detached?` | `boolean` | Detached is a live session with no window pointing at it. Live and REACHABLE are not the same thing, and treating them as one is what made the History menu useless in exactly the case you open it for. agent_detach sets the flag and closes the window but never retires the row, so a detached session is still "live" — and the menu, which hides live sessions to avoid offering to duplicate a running one, hid the one thing you were trying to get back. A detached session is not something to resume. It is something to reattach to, which is a different verb with a different outcome. |
| `row_key?` | `string` | RowKey is the roster key this session is running as, present only while it has a row. Reattach is key-addressed, not session-id addressed, so the menu needs this to offer the verb at all. |

#### SessionClaim

SessionClaim takes (or re-affirms) the controller lease on a session.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### SessionClaimed

SessionClaimed grants the lease; a session_state follows.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### SessionMeta

SessionMeta is what the history panel lists.

| Field | Type | |
|---|---|---|
| `session_id` | `string` |  |
| `agent?` | `string` |  |
| `connection?` | `string` |  |
| `catalog?` | `string` | Catalog and LaunchModel are how the session was started (the model as asked: a slot name or an id), for starting another the same way; Model is what it actually ran, from its summary. |
| `launch_model?` | `string` |  |
| `model?` | `string` |  |
| `cwd?` | `string` |  |
| `dir?` | `string` |  |
| `title?` | `string` |  |
| `user_title?` | `string` | UserTitle is the person's name for the session, when they gave one. Title above is then the SAME string — the effective title, so every reader shows the name without knowing where it came from — and this field says it was theirs. |
| `parent?` | `string` | Parent is the session that launched this one: the orchestrator of its workspace, or the member that spawned it. Empty for a session a person started. |
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

#### SlotSpec

SlotSpec is one slot as written: a model on an adapter.

| Field | Type | |
|---|---|---|
| `adapter` | `string` |  |
| `connection?` | `string` |  |
| `model?` | `string` |  |
| `effort?` | `string` |  |

#### SlotView

SlotView is one slot of a curated catalog: a model on an adapter, with its effort.

| Field | Type | |
|---|---|---|
| `slot` | `string` |  |
| `adapter` | `string` |  |
| `connection?` | `string` |  |
| `model?` | `string` |  |
| `effort?` | `string` |  |
| `available` | `boolean` |  |
| `note?` | `string` |  |

#### State

| Field | Type | |
|---|---|---|
| `version` | `number` |  |
| `rows` | `Row[] \| null` |  |
| `asks?` | `Ask[]` | Asks are permission questions waiting for a human (§12). They ride the roster's own push so the sidebar needs no second subscription. |
| `questions?` | `PendingQuestion[]` | Questions are question sets waiting for a human: an adapter's form elicitation (Claude Code's AskUserQuestion) or a workspace member's decision_request. The asker waits for the answer. |
| `recent?` | `Session[]` | Recent is the remembered session history (§13) — what a reboot or a closed window would otherwise have cost you. |
| `adapters?` | `Adapter[]` | Adapters is which agents this box can actually launch over ACP (docs/AGENT_APP.md §6). The launcher renders unavailable ones greyed with their reason rather than hiding them, so "why can I not pick Claude here" has an answer on screen. |
| `catalogs?` | `CatalogView[]` | Catalogs are what the launcher offers first (catalogs.go): each with the availability of its slots, greyed with a reason like an adapter. |
| `keys?` | `KeyView[]` | Keys are the connection keys the launcher can store: set or not, and a stored key's last four characters. Never a value. |
| `connections?` | `ConnectionView[]` | Connections are the named ways to reach an adapter (built in, and agents.json's), which a tier may name instead of the adapter direct. |
| `adapter_options?` | `AdapterOptions[]` | AdapterOptions is what each adapter last reported when a session of it started: its version, approval presets and settings (models, efforts). The Stacks tab and the launcher's Advanced and Permissions controls offer these instead of free text; an adapter that has never run here has no entry, and its fields are typed. |
| `launch` | `LaunchPrefs` | Launch is the remembered permission default the launcher starts a session with (agents.json `launch`). |
| `has_default_prompt?` | `boolean` | HasDefaultPrompt says whether a stored default prompt exists, so the launcher can say that a new session will not start empty. Only the FLAG rides the state push — the text itself is fetched on demand (agent_default prompt), because a page of prose on every roster push would reach every subscriber several times a second during a turn. |

#### Subscribe

Subscribe asks for the whole roster, now and on every change.

No fields.

#### Supervisor

Supervisor tunes the watchdog that tells the orchestrator when work has stalled (agentd workspace_supervisor.go).

| Field | Type | |
|---|---|---|
| `off?` | `boolean` |  |
| `quiet?` | `string` | Quiet is how long a turn may go without a word, a tool or a busy process before its member counts as wedged. |
| `idle?` | `string` | Idle is how long a member may sit idle with open work, or the whole team idle with the plan unfinished, before the orchestrator hears. |
| `repeat?` | `string` | Repeat is the wait before the same finding is sent again; it doubles each time. |
| `max_prompts?` | `number` | MaxPrompts is how many times the same finding is sent before the owner is told instead. |

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

#### Usage

| Field | Type | |
|---|---|---|
| `used` | `number` |  |
| `size` | `number` |  |

#### UsagePatch

UsagePatch updates rows' context accounting (Row.Used, Row.Size), coalesced to at most one send per 500ms.

| Field | Type | |
|---|---|---|
| `rows` | `UsageRow[] \| null` |  |

#### UsageRow

UsageRow is one row's counters.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `used` | `number` |  |
| `size` | `number` |  |

#### Workspace

| Field | Type | |
|---|---|---|
| `qa_authors?` | `Record<string, string>` | QAAuthors names the authors of threads read back from an earlier workspace's files, who are not members of this one. |
| `qa_dir?` | `string` | QADir is the directory holding one file per QA thread (<thread>.md), written by Wash as threads change. |
| `qa` | `QAThread[] \| null` |  |
| `approvals?` | `Rule[]` | Approvals apply to every member of this workspace, whatever its cwd. Members work in worktrees the orchestrator chooses, and those are as often siblings of project_root as children of it, so a path-scoped rule cannot cover a fleet. Membership is the scope instead: these rules carry no Cwd, and agentpolicy's matcher is reused verbatim. |
| `catalog?` | `string` | Catalog is where members' models come from (a slot name in a member's `model` resolves against it): the orchestrator's own catalog at setup, changeable with workspace_configure.catalog for later launches. |
| `id` | `string` |  |
| `name` | `string` |  |
| `project_root` | `string` |  |
| `orchestrator` | `string` |  |
| `state` | `string` |  |
| `revision` | `number` |  |
| `max_active` | `number` |  |
| `max_members` | `number` |  |
| `plan` | `Node[] \| null` | Plan is the workspace's node graph; PlanRevision counts its changes. |
| `plan_revision` | `number` |  |
| `plan_file?` | `string` | PlanFile is where Wash writes the plan as it changes (TOML). |
| `legend?` | `string` | Legend says what the orchestrator's emojis and states mean. |
| `roles?` | `Record<string, string>` | Roles are instruction templates by member role, put before a new member's own instructions (workspace.toml [roles.<role>]). |
| `context_warn?` | `number` | ContextWarn is the share of its context window at which a member's use is reported to the orchestrator, once; 0 is the default. |
| `supervisor` | `Supervisor` | Supervisor tunes the stall watchdog. |
| `nudged?` | `string[]` | Nudged are the lifecycle nudges already sent, so each goes once. |
| `members` | `Member[] \| null` |  |
| `assignments` | `Assignment[] \| null` |  |
| `messages` | `Message[] \| null` |  |

#### WorkspaceAction

WorkspaceAction is a human's action in the workspace sidebar.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `name` | `string` | Name is the operation: member_open \| member_resume \| member_message \| member_inspect. |
| `arguments` | `WorkspaceActionArgs` |  |

#### WorkspaceActionArgs

WorkspaceActionArgs are an action's arguments; each operation reads its own.

| Field | Type | |
|---|---|---|
| `member_id?` | `string` | MemberID names the member for member_open, member_resume and member_inspect. |
| `recipient?` | `string` | Recipient is the member a member_message goes to. |
| `body?` | `string` | Body is a member_message's text. |

#### WorkspaceApproval

WorkspaceApproval is a member's question waiting for the human.

| Field | Type | |
|---|---|---|
| `id` | `string` |  |
| `member_id` | `string` |  |
| `tool` | `string` |  |
| `subject` | `string` |  |

#### WorkspacePatch

WorkspacePatch changes a WorkspaceState: the frame fields and workspace fields that changed (null removes one), and the plan's nodes by id.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `base` | `number` |  |
| `sequence` | `number` |  |
| `frame` | `Record<string, unknown> \| null` | Frame holds changed WorkspaceState fields by their JSON name. |
| `workspace` | `Record<string, unknown> \| null` | Workspace holds changed swarm.Workspace fields, except the plan. |
| `plan?` | `WorkspacePlanPatch` |  |

#### WorkspacePlanPatch

WorkspacePlanPatch changes the plan: nodes replaced or added, ids removed, and the new order when it changed.

| Field | Type | |
|---|---|---|
| `upsert` | `Node[] \| null` |  |
| `remove` | `string[] \| null` |  |
| `order?` | `string[]` |  |

#### WorkspaceRefresh

WorkspaceRefresh asks for the session's workspace frame again.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |

#### WorkspaceResult

WorkspaceResult answers a WorkspaceAction.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `operation` | `string` |  |
| `error?` | `string` |  |
| `transcript?` | `WorkspaceTranscript` | Transcript answers member_inspect. |

#### WorkspaceState

WorkspaceState is the whole sidebar frame for an orchestrator's (or a member's) Agent window.

| Field | Type | |
|---|---|---|
| `key` | `string` |  |
| `sequence` | `number` | Sequence numbers this frame; a patch names the sequence it applies to. |
| `workspace` | `Workspace \| null` | Workspace is the workspace this session belongs to, or null when it belongs to none (the sidebar is then not shown). |
| `preview?` | `WorkspaceTranscript` | Preview is the transcript of the member the window has selected. |
| `activity?` | `Record<string, string>` | Activity and ActivityDetail are each member's live activity (by member id): thinking, tool use, responding, waiting, … |
| `activity_detail?` | `Record<string, string>` |  |
| `usage?` | `Record<string, Usage>` | Usage is each member's context accounting, by member id. |
| `approvals?` | `WorkspaceApproval[]` | Approvals are the members' questions waiting for the human. |
| `questions?` | `PendingQuestion[]` | Questions are the members' question sets waiting for the human. |
| `qa_markdown?` | `string` | QAMarkdown is the QA document as it is written to disk. |
| `qa_document_status?` | `QADocumentStatus` |  |
| `plan_file_status?` | `QADocumentStatus` | PlanFileStatus is where the plan file stands on disk. |

#### WorkspaceTranscript

WorkspaceTranscript is a member's recent transcript: the selected member's preview, or member_inspect's answer.

| Field | Type | |
|---|---|---|
| `member_id` | `string` |  |
| `events` | `Event[] \| null` |  |
| `asks?` | `Ask[]` | Asks are the member's questions waiting for the human, when it runs. |
| `questions?` | `PendingQuestion[]` | Questions are the member's question sets waiting for the human. |
| `note?` | `string` | Note says what the transcript is when it is not live: an ended member's archive. |

<!-- END GENERATED -->
