# Bulk workspace MCP contract

Implemented API 2.1.0, 2026-09-23. This supersedes the incremental v1 catalog in
[the original design](AGENT_SWARM.md). New discovery advertises exactly twelve
tools. Removed v1 operations return Unknown tool; there are no hidden aliases or
compatibility handlers. Agent instructions and examples use the surface below. No live desktop upgrade is implied.

## Twelve tools

| Tool | Responsibility |
| --- | --- |
| `workspace_get` | JSON state; `view:about` discovery; `view:qa` threads/generated Markdown |
| `workspace_configure` | Atomic setup/patch: profiles, settings, keyed member reservations, plan, document |
| `workspace_end` | End children/detach sidebar; preserve owning conversation, files and history |
| `member_control` | Pause/resume/end IDs, keys or a package; per-member outcomes |
| `member_update` | Atomic own status/emoji/waiting, acknowledgments, results and QA updates |
| `message_send` | One message or an atomic batch; optional QA opening/thread linkage |
| `inbox_read` | Paginated caller inbox |
| `inbox_ack` | Atomic message acknowledgment batch |
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
  "profiles":{"worker":{"provider":"codex","model":"<advertised ID>","thinking":"high"}},
  "members":{"K5-red":{"name":"K5 red","profile":"worker","cwd":"/data/project-worktree","lifetime":"resident","package":"K5","role":"reviewer","instructions":"Review the package defensively; do not edit. Wait for assignments."}},
  "plan":{"items":{"K5":{"text":"Accept K5","state":"active"}}},
  "document":{"path":"/data/project/docs/BUILD-PLAN.md","title":"Build plan"},
  "qa_document":{"path":"/data/project/docs/WORKSPACE-QA.md","title":"Project QA"}
}
```

- Omitted fields stay. Profiles merge by alias; each object replaces the alias,
  null deletes. Model IDs/thinking values must come from provider choices.
- Members use stable keys; an existing matching definition is reused. Profile edits
  affect future launches. Changed member definitions or ended keys require explicit
  end/replacement with a new key. No implicit restart or termination.
- Keyed plan objects patch individual fields, null removes; optional order must list
  every remaining ID exactly once. document:null detaches the live Markdown document.
- `expected_revision` guards workspace edits (0 may guard initial setup). A conflict
  requires fresh state and reconciliation. QA has separate per-thread revisions.
- `preview:true` validates/stages without writing or launching. It does not prove
  provider availability or adapter support; launch validates model before thinking.
- Configuration and member reservations commit atomically. Processes start afterward,
  with separate `launches` outcomes. Check each; launch failure does not undo config.
  Explicit member_control resume can retry a failed reserved launch.
- `request_id` deduplicates identical configuration, reporting, message, assignment,
  acknowledgment and decision requests. Receipts persist with state. Reusing an ID
  with different arguments fails. Preview does not consume a request ID. The config
  receipt is the original commit; launch outcomes reflect current state.
- Bulk store changes either all persist or none do. Process controls return per-member
  outcomes because process side effects cannot be rolled back. Retry uncertain delivery
  only after reconciliation; this is not an exactly-once model-execution guarantee.

Stable member keys/IDs are accepted by message recipients, assignments, QA assignees
and member_control. Keys `conversation`, `plan`, `qa` are reserved. Workspace configuration
is orchestrator-only; CanSpawn grants assignment authority, not config
ownership. All members can communicate. Process control retains existing authority checks.

## Resident package workflow

The orchestrator and Architect stay resident. Each active package keeps its implementer
and defensive/simplifier/editor reviewers resident through repeated review/fix assignments.
Completing an assignment leaves residents available. Ephemeral members retire after their
assignment and turn finish. Explicit `member_control` ends package residents at acceptance
or abandonment. Limits count idle residents; they do not consume model turns while waiting.

`member_update` can acknowledge messages, complete assignments, update status and set
`waiting:{reason,...}` together. `waiting.until_assignments` lists assignments the caller
created: their results are held and delivered together, in one turn, once the last one
completes or fails, so a review round wakes the orchestrator once rather than once per
reviewer. A complete/fail result may `cc` members, who get a non-waking progress copy; a
reviewer cc's the implementer so findings need not be retyped. Every inbox turn carries its
messages as one JSON array. A queued assignment instruction whose assignment the assignee
already resolved (it read the task early with inbox_read) is dropped, not re-delivered. Waiting returns immediately with an instruction to end
the turn; actionable messages wake the member in a later turn. Never poll. Acknowledgment
is not completion. question/answer/instruction wake; progress records without waking.

## QA: one writer, concurrent append

Wash owns durable QA threads and generates the live Questions Markdown. The sidebar
shows open threads, blocking state and next responder; selecting one opens the main-panel
Questions tab at its heading. Configure `qa_document:{path,title}` at setup. Wash creates
the file and atomically replaces it after each QA update or human answer; the full file
includes all events, IDs and timestamps. The bounded tab preview shows the configured
path and file-write errors. No agent rewrites that generated file or commits per reply.

Relative output paths resolve from the project root; parent directories must exist.
Specify a .md filename on creation. An existing file is loaded: Wash files contain a
versioned checkpoint that restores threads, revisions, attribution and pending owner
questions even without the old backend store. Ordinary Markdown is preserved verbatim
in the live document; it is not guessed into structured threads. A corrupt/unsupported
checkpoint fails without changing the file. Restore reads are limited to 64 MiB.
Unfinished questions are assigned to the new orchestrator, who assigns the current team;
historical agent identities do not relaunch sessions. A retained backend record takes
precedence over its stale export. Symlinks, the plan path, and another active workspace's
QA file/document identity are rejected. Completed runs transfer projection ownership.
`qa_document:null` detaches output without deleting history or files. Configuration preview
never writes the file. The backend remains authoritative: a projection failure does not
undo a committed QA update. Read `qa_document_status` (saved/pending/error) in tool results
or the Questions tab; the service retries failures and reconstructs output after restart.
Saved status includes saved_revision. Changing the configured path leaves the old file intact. Output continues without an open tab.

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
| resolve | expected_revision, evidence; only orchestrator or reviewer tagged to that package |
| reopen | expected_revision, reason in body |

Transitions also accept decision_refs and blocking where applicable. Only the creator,
assignee, orchestrator or package reviewer may transition; resolution is stricter.
There is no delete/edit-history operation. A resolved thread must be reopened before replies.
Thread transitions do not themselves wake an assignee: send a linked actionable message.

Link `decision_request` to thread_id. The human's GUI answer is recorded with human
authority in the same transaction as the response; an agent cannot fabricate that event.
Pending decisions prevent resolution. An answer does not resolve QA. The Architect writes
formal project decisions/specifications, links them through decision_refs and routes the
implementation back. Package reviewers verify evidence before closure. Project acceptance
policy requires no unresolved blocking QA; Wash does not infer whether a git merge satisfies it.

`workspace_get({"view":"qa","package":"K5"})` returns summaries/generated Markdown;
`thread_id` selects events, paginated with after/limit. General state and reporting receipts
omit QA event bodies. The rendered view shows recent events and is bounded; use paginated
readback for complete history. Limits: 500 threads/workspace, 1,000 events/thread, 32 KiB
body/evidence, 100 items/batch; idempotency storage is capped at 10,000 retained receipts.
Commit the automatically maintained Markdown with ordinary project tools when appropriate.
There is no automatic Git commit or live two-way synchronization of manual edits. Reopening imports the checkpoint; agents must not edit generated history.

## Persistence and approval boundary

Browser refresh reconnects to server-owned state; child sessions keep running. Backend
restart retains QA, inboxes and receipts but pauses recovered members for deliberate resume.
Uncertain delivery remains explicit. Tabs/drafts are local and not restored. Teardown retains
backend history, attempts the final QA save and returns qa_document_status. Failed exports continue retrying after teardown and backend restart, and show a desktop error.
`workspace_get` selects the current attached workspace and has no archive selector.

Member transcript tabs expose pending approval controls through the existing human answer
route. Bulk tools reduce permission round trips; they do not change permission authority.
Reviewer role instructions and adapter mode names do not enforce a filesystem sandbox.
Set `capability:"reviewer"` in a profile or member definition for the explicit restricted
launch, for example:

```json
{"profiles":{"review":{"provider":"claude","capability":"reviewer"}}}
```

This currently requires verified `@agentclientprotocol/claude-agent-acp` 0.79.0 or 0.81.1;
Codex, Gemini and unverified versions fail launch with an actionable error. Inspect
about.permissions.reviewer_capability_profiles before choosing a provider. Codex's
`read-only` adapter mode uses workspaceWrite and cannot satisfy this contract.

The Claude profile uses its provider tool allowlist (Read/Glob/Grep), disables ordinary
settings/hooks and unrelated MCP servers, and refuses write/terminal callbacks in Wash.
Only scoped coordination tools are approved automatically; explicit host policy denies
still win. Workspace configuration, lifecycle control and spawning are unavailable.
These are provider tool restrictions, not an OS sandbox or a claim about trusted managed
hooks. Restrictions are immutable for a session and reapplied on resume; model/thinking
remain configurable. No broad auto-approval or role-prompt enforcement is substituted.

Wash's own `wash_workspace` coordination calls never ask the human, for any member: the
bridge derives the caller from session credentials and enforces every role limit itself.
Explicit host policy denies still win. Anything else a member does follows the host policy,
then workspace-scoped rules, then the member's auto-approval, then the human.

`approval:"auto"` on a profile or member definition launches that member with host
auto-approval on (every approval narrated in its transcript; host policy denies still
win). It is a grant, so only a session that is itself auto-approved can configure one:
children stay within the launcher's authority. It cannot be combined with
`capability:"reviewer"`. A member's auto-approval, set at launch or by the human's toggle
on its tab, is kept on the member record, restored when a restart's paused member is
resumed, and ends with the workspace.

The sidebar's Needs you section shows pending approvals across all members, owner
questions and QA save errors before plan/team navigation. Approval links open the correct
member tab, where existing human controls answer the request.

## Injected instructions

`internal/workspacemcp/about.go` owns the concise Instructions string shared by MCP
initialization and about. It covers discovery, bulk reconciliation, keyed launches, resident
lifetimes, QA, human decisions/approvals, waiting, uncertain delivery and deliberate teardown.
Children additionally receive their supplied role instructions and a short membership suffix;
every inbox turn has a server-authored identity prefix and serialized attributed message.
Children have fresh provider context, not the parent's transcript or launcher default prompt.
See [Redoubt's complete operating example](examples/redoubt-workspace.md).
