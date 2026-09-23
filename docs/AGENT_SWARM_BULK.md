# Bulk workspace MCP contract

Implemented API 2.0.0, 2026-09-23. This supersedes the incremental v1 catalog in
[the original design](AGENT_SWARM.md). New discovery advertises exactly twelve
tools. Hidden v1 operations remain accepted for existing conversations; new agent
instructions and examples use the surface below. No live desktop upgrade is implied.

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
  "document":{"path":"/data/project/docs/BUILD-PLAN.md","title":"Build plan"}
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
is orchestrator-only; CanSpawn grants assignment/legacy spawning authority, not config
ownership. All members can communicate. Process control retains existing authority checks.

## Resident package workflow

The orchestrator and Architect stay resident. Each active package keeps its implementer
and defensive/simplifier/editor reviewers resident through repeated review/fix assignments.
Completing an assignment leaves residents available. Ephemeral members retire after their
assignment and turn finish. Explicit `member_control` ends package residents at acceptance
or abandonment. Limits count idle residents; they do not consume model turns while waiting.

`member_update` can acknowledge messages, complete assignments, update status and set
`waiting:{reason,...}` together. Waiting returns immediately with an instruction to end
the turn; actionable messages wake the member in a later turn. Never poll. Acknowledgment
is not completion. question/answer/instruction wake; progress records without waking.

## QA: one writer, concurrent append

Wash owns durable QA threads and generates the live Questions Markdown. The sidebar
shows open threads, blocking state and next responder; selecting one opens the main-panel
Questions tab at its heading. No agent rewrites a shared QA file or commits per reply.

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
Export/commit accepted review evidence with ordinary project tools; no automatic file
exporter or Git ownership mechanism is implemented.

## Persistence and approval boundary

Browser refresh reconnects to server-owned state; child sessions keep running. Backend
restart retains QA, inboxes and receipts but pauses recovered members for deliberate resume.
Uncertain delivery remains explicit. Tabs/drafts are local and not restored. Teardown retains
backend history; read/export project evidence before detaching, since workspace_get selects
the current attached workspace and has no archive selector.

Member transcript tabs expose pending approval controls through the existing human answer
route. Bulk tools reduce permission round trips; they do not change permission authority.
Reviewer role instructions and adapter mode names do not enforce a filesystem sandbox.
Scoped reviewer capability profiles remain unsupported and discovery says so. Do not grant
broad auto-approval to bypass coordination prompts. Future enforcement needs structured MCP
server/tool policy plus explicit supported read/write/execute restrictions.

## Injected instructions

`internal/workspacemcp/about.go` owns the concise Instructions string shared by MCP
initialization and about. It covers discovery, bulk reconciliation, keyed launches, resident
lifetimes, QA, human decisions/approvals, waiting, uncertain delivery and deliberate teardown.
Children additionally receive their supplied role instructions and a short membership suffix;
every inbox turn has a server-authored identity prefix and serialized attributed message.
Children have fresh provider context, not the parent's transcript or launcher default prompt.
See [Redoubt's complete operating example](examples/redoubt-workspace.md).
