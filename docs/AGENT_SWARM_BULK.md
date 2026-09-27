# Bulk workspace MCP contract

Implemented API 3.3.0, 2026-09-24. This supersedes the incremental v1 catalog in
[the original design](AGENT_SWARM.md). New discovery advertises exactly eleven
tools. Removed v1 operations return Unknown tool; there are no hidden aliases or
compatibility handlers. Agent instructions and examples use the surface below. No live desktop upgrade is implied.

API 3 removed `inbox_ack` and `member_update.acknowledge`: a message is delivered when the
turn carrying it ends cleanly, so acknowledging was an extra full-context request per wake-up.
Results, and a member's messages to the orchestrator, are summaries of at most 2000 bytes
(`swarm.ReportLimit`): they are re-read on every later turn of the receiver, so detail goes
in the QA thread or a file.

API 3.1 adds plan-mode control. `member_control` `configure` (orchestrator only) changes a live
member's adapter settings, e.g. `configs:{"mode":"default"}` once its plan is approved; the change
is recorded as the member's `adjusted_configs`, beside its unchanged keyed launch definition, and
reapplied on every resume. A member's request to leave plan mode (ACP tool kind `switch_mode`) is
always declined, before any policy rule or auto-approval. claude-agent-acp answers that refusal by
ending the turn, so wash treats the stop like `interrupt` (member available, mail delivered),
saves the plan from the request under `$XDG_STATE_HOME/wash/workspace-plans/`, and wakes the
orchestrator with a question carrying the plan's start, its path and the approving call. `interrupt` ends a member's current turn and leaves it
available, with what the turn carried delivered; `pause` still also stops dispatch. A
member `subagents:"deny"` removes Claude's own Agent/Task tool at launch and on resume; adapters
wash cannot restrict fail to launch.

API 3.2 lets a session end a stale workspace. `workspace_end` with `workspace_id` (the full ID or
a unique prefix of at least 8) ends another open workspace whose orchestrator session is not
running in Wash, e.g. one whose reopen failed and that still holds its QA file. The orchestrator
of a workspace, or a session in none, may do this; a member may not, and a workspace whose
orchestrator is running is refused. A QA-file conflict names the holding workspace and this call.
A member's role instructions and initial task are now one first message, with the task under
`## Your assignment (<id>)`; together they must fit the 32 KiB message limit, checked at configure.
Sent as two, the role went out alone and was taken as the go-ahead before the task's "plan first"
arrived. A member launched without a task is told to wait for its assignment.

API 3.3 adds catalogs (renamed from stacks and tiers on 2026-09-25). A catalog
(apps/agentd/be/catalogs.go) is a named, global source of models: an adapter's own list, or
three slots, `frontier`, `coding` and `small`, each an adapter, connection, model and effort,
and nothing else; the launcher starts sessions from them. A workspace takes its `catalog` from
the orchestrator's own session (the catalog it was started from, or its adapter's own list),
and `workspace_configure.catalog` changes it for later launches. A member definition gives
`"model":"coding"` (a slot) or a model id, never a marketing name: the slot is resolved into the
member's launch settings when its key is reserved, so a later catalog edit changes no running
member. A member may name another `catalog` of its own. Explicit `effort` and `configs` sit on
top of the slot's; with no `model` the catalog's default slot (frontier) or the adapter's default
applies. Members never need a model string, which matters because members run on cheap models.
A slot is a model only: a reviewer that must not write says `capability:"reviewer"` beside its
`model`, and only Claude Code enforces that (about.permissions); elsewhere a reviewer is
read-only by instruction. The per-workspace `profiles` map and `default_profile` are gone.

## Eleven tools

| Tool | Responsibility |
| --- | --- |
| `workspace_get` | Compact team view by default; `view:state` full JSON; `view:about` discovery; `view:qa` threads/generated Markdown |
| `workspace_configure` | Atomic setup/patch: catalog, settings, keyed member reservations, plan, document |
| `workspace_end` | End children/detach sidebar; preserve owning conversation, files and history; `workspace_id` ends a stale workspace |
| `member_control` | Orchestrator only: pause/resume/interrupt/end IDs, keys or a package; `configure` of live settings; per-member outcomes |
| `member_update` | Atomic own status/emoji/waiting, results and QA updates |
| `message_send` | One message or an atomic batch; optional QA opening/thread linkage |
| `inbox_read` | Paginated caller inbox history |
| `message_retry` | Explicit reconciliation/retry of uncertain delivery |
| `assignment_update` | Atomic create/complete/fail batch |
| `decision_request` | Actual human choice, optionally linked to QA |
| `flash_message` | Attributed desktop notification |

`tools/list` supplies full schemas. `workspace_get({"view":"about"})` works before
setup, returns the API/tool list, implemented capabilities, operating instructions,
caller identity/model choices and honest provider/host permission metadata.
It does not create a workspace. Default state includes members, settings, assignments,
plan and QA summaries; transcript bodies are read explicitly with pagination.

## Configuration and retries

```json
{
  "request_id":"package-setup-1",
  "workspace":{"name":"Project","project_root":"/data/project"},
  "catalog":"openai-pro",
  "members":{
    "K5-impl":{"name":"K5 implementer","model":"coding","cwd":"/data/project-worktree","lifetime":"resident","package":"K5","role":"implementer","instructions":"Implement K5. Wait for assignments."},
    "K5-red":{"name":"K5 red","model":"small","effort":"high","cwd":"/data/project-worktree","lifetime":"resident","package":"K5","role":"reviewer","instructions":"Review the package defensively; do not edit. Wait for assignments."}},
  "plan":{"items":{"K5":{"text":"Accept K5","state":"active"}}},
  "document":{"path":"/data/project/docs/BUILD-PLAN.md","title":"Build plan"},
  "qa_dir":".wash/qa"
}
```

- Omitted fields stay. A member's `model` names a slot of the workspace catalog, or a
  model id that must come from provider choices. An adapter's own list (`anthropic`)
  has no slots: `model:"coding"` on it fails, in preview too, naming the catalogs with
  slots for that adapter.
- A member's `cwd` defaults to `project_root`, and a relative one is inside it; never
  the orchestrator's own folder.
- Members use stable keys; an existing matching definition is reused. Catalog edits
  affect future launches. Changed member definitions or ended keys require explicit
  end/replacement with a new key. No implicit restart or termination. The exception
  is a member whose launch failed before it had a session: its key takes a corrected
  definition in place (same member ID), and it holds no name.
- Keyed plan objects patch individual fields, null removes; optional order must list
  every remaining ID exactly once. document:null detaches the live Markdown document.
- `expected_revision` guards workspace edits (0 may guard initial setup). A conflict
  requires fresh state and reconciliation. QA has separate per-thread revisions.
- `preview:true` validates/stages without writing or launching. It does not prove
  provider availability or adapter support; launch applies mode, then model, then effort
  (Claude Code re-picks the model on a mode change and narrows effort by model).
  A preview returns only ids that already exist: a new workspace's id and new
  members' ids are empty, so address new members by key.
- Configuration and member reservations commit atomically. Processes start afterward,
  with separate `launches` outcomes. Check each; launch failure does not undo config.
  Explicit member_control resume can retry a failed reserved launch.
- `request_id` deduplicates identical configuration, reporting, message, assignment
  and decision requests. Receipts persist with state. Reusing an ID
  with different arguments fails. Preview does not consume a request ID. The config
  receipt is the original commit; launch outcomes reflect current state.
- Bulk store changes either all persist or none do. Process controls return per-member
  outcomes because process side effects cannot be rolled back. Retry uncertain delivery
  only after reconciliation; this is not an exactly-once model-execution guarantee.

Stable member keys/IDs are accepted by message recipients, assignments, QA assignees
and member_control. Keys `conversation`, `plan`, `qa` and `orchestrator` are reserved; the orchestrator is addressed as `orchestrator`. Workspace configuration
is orchestrator-only; CanSpawn grants assignment authority, not config
ownership. All members can communicate. Process control retains existing authority checks.

## Resident package workflow

The orchestrator and Architect stay resident. Each active package keeps its implementer
and defensive/simplifier/editor reviewers resident through repeated review/fix assignments.
Completing an assignment leaves residents available. Ephemeral members retire after their
assignment and turn finish. Explicit `member_control` ends package residents at acceptance
or abandonment. Limits count idle residents; they do not consume model turns while waiting.

`member_update` can complete assignments, update status and set
`waiting:{reason,...}` together. `waiting.until_assignments` lists assignments the caller
created: their results are held and delivered together, in one turn, once the last one
completes or fails, so a review round wakes the orchestrator once rather than once per
reviewer. A complete/fail result may `cc` members, who get a non-waking progress copy; a
reviewer cc's the implementer so findings need not be retyped. Every inbox turn carries its
messages as one JSON array. A queued assignment instruction whose assignment the assignee
already resolved (it read the task early with inbox_read) is dropped, not re-delivered. Waiting returns immediately with an instruction to end
the turn; actionable messages wake the member in a later turn. Never poll. Acknowledgment
is not completion. question/answer/instruction wake; progress records without waking.

## QA: one file per thread

Wash owns durable QA threads and writes each to its own Markdown file. The sidebar
shows open threads, blocking state and next responder; selecting one opens the main-panel
Questions tab at its heading. Configure `qa_dir` at setup (`".wash/qa"`, relative to the
project root; created if missing). Each thread is `<qa_dir>/<thread>.md`: a marker line
naming the thread, the thread as Markdown (every event with its ID and time), and a
checkpoint (the thread as JSON). A file is written only when its thread changes, so a
resolved thread stops changing and a commit's diff shows only the threads it touched.
Paths under the home directory and the project root are written as `~` and `.`. No agent
edits those files or commits per reply.

A thread body, reply, evidence or thread message is at most 2000 bytes: the thread holds
the pointer, a file under version control holds the detail. (DOC1's single QA file reached
2 MB because whole plans and review reports went into thread bodies.)

Set on a new workspace, `qa_dir` resumes the threads found there, without the old backend
store: open threads come back whole and are assigned to the new orchestrator, who assigns
the current team; resolved threads come back as headers only (title, state, decision
references, evidence), marked `resumed` because their evidence is about the earlier code,
and their events stay in their files until `view=qa thread_id` reads them or a reopen loads
them. Pending owner decisions are asked again. Other Markdown in the directory is ignored and
never overwritten; a damaged Wash file stops the configuration, naming the file, and nothing
changes. Symlinks are not written through. Another active workspace's directory is refused,
naming it; an ended workspace's directory is taken over. `tools/qa-split` converts an API 3
single-file QA document into thread files, once.

`qa_dir:null` detaches output without deleting history or files. Configuration preview
never writes. The backend remains authoritative: a failed write does not undo a committed
QA update. Read `qa_document_status` (saved/pending/error, with the directory as its path)
in tool results or the Questions tab; the service retries failures, also after teardown,
and rewrites a file removed behind its back.

Open and deliver atomically:

```json
{"request_id":"q-open","recipient":"architect","type":"question","body":"Which approved bound applies?","qa":{"action":"open","id":"K5-clock","package":"K5","title":"Clock bound","blocking":true}}
```

Use `thread_id` on later messages and `reply_to` for correlation. Wash supplies event IDs,
author and timestamp. QA updates through `member_update.qa_updates` support:

| Action | Requirement |
| --- | --- |
| open | Unique id, package, title, body, active assignee (message_send defaults recipient) |
| reply | Body; append without revision guard |
| assign | expected_revision, next assignee |
| block | expected_revision; marks blocking |
| resolve | expected_revision, evidence; only the orchestrator, or the reviewer whose package is the thread's |
| reopen | expected_revision, reason in body; the thread returns to the orchestrator until reassigned |

Transitions also accept decision_refs and blocking where applicable. Only the creator,
assignee, orchestrator or the reviewer whose package is the thread's may transition;
resolution is stricter: the orchestrator, or the reviewer whose package is the thread's.
A reviewer assigned to review every package is not that reviewer for any of them.
There is no delete/edit-history operation. A resolved thread must be reopened before replies.
A thread resumed from a QA directory is the new orchestrator's. A resolved one is marked
`resumed` (its view says "Resolved in an earlier workspace"): its evidence is about the earlier
code, so reopen any the current code may contradict.
A reply or a resolution through `qa_updates`, and an `answer` sent on a thread, reach the
thread's creator and assignee (other than the author and the message's recipient) as an
`answer`, which wakes them: a member blocked on a thread wakes when it is answered, whoever
the answer was addressed to. An `assign` sends the new assignee a question naming the thread.

Link `decision_request` to thread_id. The human's GUI answer is recorded with human
authority in the same transaction as the response; an agent cannot fabricate that event.
Pending decisions prevent resolution. An answer does not resolve QA. The Architect writes
formal project decisions/specifications, links them through decision_refs and routes the
implementation back. Package reviewers verify evidence before closure. Project acceptance
policy requires no unresolved blocking QA; Wash does not infer whether a git merge satisfies it.

`workspace_get({"view":"qa"})` returns the index: one row per thread (id, package, title,
state, assignee, revision); `package` filters it. `thread_id` selects a thread's events,
paginated with after/limit. General state and reporting receipts omit QA event bodies.
Limits: 500 threads/workspace, 1,000 events/thread, 2000 bytes per body/evidence, 100
items/batch; idempotency storage is capped at 10,000 retained receipts. Commit the thread
files with ordinary project tools at acceptance; there is no automatic Git commit.

## Persistence and approval boundary

Browser refresh reconnects to server-owned state; child sessions keep running. Backend
restart retains QA, inboxes and receipts but pauses recovered members for deliberate resume.
Uncertain delivery remains explicit. Tabs/drafts are local and not restored. Teardown retains
backend history, attempts the final QA save and returns qa_document_status. Failed exports continue retrying after teardown and backend restart, and show a desktop error.
`workspace_get` selects the current attached workspace and has no archive selector.

Member transcript tabs expose pending approval controls through the existing human answer
route. Bulk tools reduce permission round trips; they do not change permission authority.
Reviewer role instructions and adapter mode names do not enforce a filesystem sandbox.
Set `capability:"reviewer"` in a member definition for the explicit restricted launch, for
example:

```json
{"members":{"K5-red":{"name":"K5 red","model":"coding","capability":"reviewer","lifetime":"resident","instructions":"…"}}}
```

This currently requires verified `@agentclientprotocol/claude-agent-acp` 0.81.1 or 0.81.2;
Codex, Gemini and unverified versions fail launch with an actionable error. Inspect
about.permissions.reviewer_capability_profiles before choosing a provider. Codex's
`read-only` adapter mode uses workspaceWrite and cannot satisfy this contract.

The Claude reviewer uses its provider tool allowlist (Read/Glob/Grep), disables ordinary
settings/hooks and unrelated MCP servers, and refuses write/terminal callbacks in Wash.
Only scoped coordination tools are approved automatically; explicit host policy denies
still win. Workspace configuration, lifecycle control and spawning are unavailable.
These are provider tool restrictions, not an OS sandbox or a claim about trusted managed
hooks. Restrictions are immutable for a session and reapplied on resume; model/effort
remain configurable. No broad auto-approval or role-prompt enforcement is substituted.

Wash's own `wash_workspace` coordination calls never ask the human, for any member: the
bridge derives the caller from session credentials and enforces every role limit itself.
Explicit host policy denies still win. Anything else a member does follows the host policy,
then workspace-scoped rules, then the member's auto-approval, then the human.

A member's approval defaults to its launcher's (2026-09-25): a member with no
`approval` of its own is launched auto-approved exactly when the session launching it is,
and follows the orchestrator's later yolo toggles, each change narrated in the member's
transcript. So yolo on the orchestrator is yolo for the workspace. `approval:"ask"` opts a
member out; `approval:"auto"` launches it auto-approved regardless, which is a grant, so
only a session that is itself auto-approved can configure one: children stay within the
launcher's authority. Neither applies to `capability:"reviewer"`, which never inherits and
cannot be combined with `auto`. A member's auto-approval, set at launch or by the human's
toggle on its tab, is kept on the member record, restored when a restart's paused member is
resumed, and ends with the workspace.

The sidebar's Needs you section shows pending approvals across all members, owner
questions and QA save errors before plan/team navigation. Approval links open the correct
member tab, where existing human controls answer the request.

## Injected instructions

`internal/workspacemcp/about.go` owns the concise Instructions string shared by MCP
initialization and about. It covers discovery, bulk reconciliation, keyed launches, resident
lifetimes, QA, human decisions/approvals, waiting, uncertain delivery and deliberate teardown.
Children additionally receive, as one first message, their supplied role instructions, a short
membership suffix and their initial task (or, without one, an instruction to wait for it);
every inbox turn has a server-authored identity prefix and serialized attributed message.
Children have fresh provider context, not the parent's transcript or launcher default prompt.
See [Redoubt's complete operating example](examples/redoubt-workspace.md).
