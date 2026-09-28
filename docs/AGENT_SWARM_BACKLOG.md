# Agent workspace backlog

Status: rewritten 2026-09-26 on the `workspace-plan` branch, after the Redoubt
DOC1 run (about 20 members, two days) and a design discussion with the owner.
The design as built is in [AGENT_SWARM.md](AGENT_SWARM.md) and
[AGENT_SWARM_BULK.md](AGENT_SWARM_BULK.md). This file lists what is agreed and
not yet built, with the API shapes. When an item is done, move its design into
those documents, delete it here, and record the change in its commit message.

The working assumption behind most items: **the orchestrator is a top-tier
model working with the human, and members (implementer, simplifier,
reviewers, red team) run on cheap models.** Anything the orchestrator reads
can be dense. Anything a member reads must be short, unambiguous and hard to
misuse.

## The shape

A workspace has three formal mechanisms and one piece of runtime state:

| Mechanism | What it is | Who writes it |
|---|---|---|
| **The plan** | A graph of nodes: milestones, packages, steps, notes, however the orchestrator uses them. The backbone: every assignment and member hangs off a node. (done: AGENT_SWARM_BULK.md, "The plan") | The orchestrator (the Architect for unstarted nodes); Wash moves states as assignments open and close |
| **Questions between agents** | QA threads, one file each, the record of why (done: AGENT_SWARM_BULK.md, "QA: one file per thread") | Agents, through the tools |
| **Questions to the owner** | Structured questions that block the asker until the owner answers; the answer is recorded verbatim in the QA thread (done: AGENT_SWARM_BULK.md, `decision_request`; AGENT_APP.md §7) | Agents ask, the owner answers in a panel |
| Runtime | Who is working, running a tool, waiting on a background task, or needs you (done) | Wash, from the sessions |

Work state lives in Wash and in the project's `.wash/` directory, never only in
the orchestrator's context: a compaction or a new orchestrator reads the plan
back instead of reconstructing it.

## Decisions taken with the owner (2026-09-26)

- Wash enforces that the plan is true (every piece of work on a node, needs
  gate the start with a recorded override); it does not enforce a flow
  (stage types, order, review counts).
- `decision_request` blocks the asker.
- QA stays the record of why (revisioned threads, resolve rights, evidence);
  the old "shrink QA to questions" idea is dropped.
- Assignments stay one active per member; no queues.
- The Architect may edit nodes that have not started; running work is the
  orchestrator's. (Confirmed by the owner 2026-09-27.)
- `plan.toml` and thread files are committed at accept and wave end by the
  orchestrator, from the paths Wash returns. (Confirmed by the owner 2026-09-27.)
- Plan approval by the owner is the orchestrator's call (a `decision_request`),
  not enforced.

**Dropped:** a fresh reviewer each round; routing everything through the
orchestrator; "the last message is the result"; shrinking QA; assignment
queues; a reading-list field; a structured-results schema (accept reads
result text); the project layout as a Wash concern. **Deferred:** checks run
by Wash, an automatic worktree per node, best of N, a Wash `ask` tool for
sessions without elicitation (codex, opencode; check codex-acp first).

## Execution on `workspace-plan`

Order (all done on this branch; the live shakedown is what remains): F, A with B, then C with D and E, then G, then H. Each step
lands with its unit tests, its e2e changes, the docs (AGENT_SWARM,
AGENT_SWARM_BULK, AGENT_PROTOCOL via `make gen-agent-protocol`) and an entry
removed here. The MCP API version goes to 4.0.0: `package`, `packages`,
`plan.items`, `document` and `qa_document` are removed, not aliased.

**Proof.**

- `e2e/tests/agent-workspace-plan.spec.ts` drives the whole flow on the fake
  adapter, deterministically: the fake orchestrator is the spec typing tool
  calls; fake members follow keywords in their task (`REJECT_ONCE`,
  `ASK_OWNER`, `ASK_PEER`, `BACKGROUND`, `FAIL`).
- `e2e/shakedown/` is a test project for a live run on cheap models. The work
  is trivial (one-line files under `words/`); each step of its `SCRIPT.md`
  exercises one feature, says what to expect, and `check.sh` verifies the end
  state (files, plan states, thread files). `make shakedown` copies it to a
  fresh git repository under `/tmp` and prints what to paste into an
  orchestrator.

| Step | Exercises |
|---|---|
| 1. Configure from `.wash/workspace.toml`; plan three milestones: Plan, Build, Ship | setup from file, sketch milestones, the Plan tab |
| 2. Plan goes active: expand Build into A, B, C (C needs A and B) | `plan_set` upsert, needs, the graph |
| 3. Assign C early | refusal, then `override` with a reason |
| 4. A's implementer writes `alpha.txt`; its reviewer rejects round 1 | active → reported → reopened → done |
| 5. B's implementer asks A's implementer through QA | a thread file; the asker woken by the answer; resolution |
| 6. B's implementer runs `sleep 30` in the background | the Background state |
| 7. C's implementer asks the owner two questions (a choice and free text) | the blocking panel, "needs you" badges, the answer in the thread |
| 8. A member fails on purpose | `failed`, reassignment |
| 9. End a member while its node is active | the "nobody on it" nudge |
| 10. `plan_accept(A)` | trailers and paths to stage |
| 11. Build done | the next-milestone nudge |
| 12. `workspace_end` with Ship active; resume in a new workspace | the end check; plan and QA reload, resolved threads as headers |
| 13. The owner asks "status?" | the answer comes from `plan_get` |

**Live runs (2026-09-26),** `WASH_E2E_LIVE=1` with Sonnet orchestrating and
Haiku members, the test answering as the owner:

- Run 1 (3.6 min): every check passed. Deviation: nudges caused by the
  orchestrator's own calls (ending a member, finishing M2) were queued and
  reached it only after its turn. Fixed: they come back in that call's result.
  The orchestrator also flattened the trailers onto one line; `check.sh` now
  checks them as git parses them, and `plan_accept` says one per line.
- Run 2 (9.8 min): every check passed, every nudge arrived, the trailers parse.
  Nothing told the orchestrator when a member started waiting on the owner, so
  step 9 (end a member while its question is pending) cost it about five
  polls. Now it gets a note, which rides with its next turn, and wakes it
  when it is idle in a plain wait: GLM-5.3 kept to "do not poll" and sat at
  step 9 until its deadline while the note never woke it (2026-09-27). The
  Haiku member also
  spent minutes re-reading the whole QA view before asking; its instructions
  could point it at its thread.

## From Redoubt on API 4 (2026-09-27)

Redoubt moved to `workspace.toml`, `plan.toml` and `qa/`; the first hour
found these.

- **The QA checkpoint is two thirds of every thread file (M, med).** After
  `tools/qa-split` and one resume, `.wash/qa/` was 95 files and 2.7 MB, more
  than the 2.1 MB single file it replaced; 1.8 MB of it is the
  `wash-qa-checkpoint-v2` comment (`internal/swarm/qafile.go`): the whole
  thread again as base64 JSON, rewritten whole on any change, opaque in
  diffs. Keep in it only what the Markdown cannot carry losslessly (author
  IDs, message IDs, flags), or make the Markdown the parsed form; if it
  stays, plain JSON in a fenced block, not base64.
- **`qa-split` writes every author into every file (S, low).** Each split
  thread carries the whole old document's `authors` map (126 members, about
  5 KB a file); Wash's own writes carry only the thread's. Keep only the
  thread's authors, and re-split or rewrite Redoubt's files.
- **Resume reassigns open threads silently (S, low).** 32 of Redoubt's 95
  threads were open (most answered long ago); resume gave each to the
  orchestrator with "Resumed from the QA directory"
  (`workspace_qa_resume.go`), and nothing in the setup result said so.
  Return `qa_resumed: {open: [ids], resolved: n}` from `workspace_configure`,
  and have `plan_get` list open threads on no node, so stale ones get
  triaged rather than carried forward.
- **Report the swallowed prompt upstream (S, low).** claude-agent-acp 0.81.2
  loses a `session/prompt` sent while Claude Code runs a turn of its own
  (reproduction: a background `sleep`, then a prompt during the woken
  turn's foreground tool). Wash no longer sends into such a turn
  (AGENT_APP.md, "Turns the agent starts itself"); the adapter should still
  settle or refuse it.

# Earlier backlog, kept for reference

The member-surface items (section 1) are to be reassessed once the plan and
the question mechanisms have changed the member's tools. The simplifications
(section 3) are still wanted where A–H do not absorb them.

## 1. Member surface for cheap models

The highest-value work. Items 1.1–1.6 fit in one change set.

### 1.1 Separate member instructions (S, low)

`internal/workspacemcp/protocol.go:117` sends the orchestrator's `Instructions`
to everyone. Members read about `workspace_configure`, `request_id`,
uncertain deliveries, capability profiles and `view:"about"`. Add
`MemberInstructions` and choose it by `MemberEnv`, as `Run` already chooses
the tool list. Draft:

> You are a member of a Wash workspace. Your role and task are in your first
> message. New messages arrive as new turns: never poll, never acknowledge.
> Finished or stuck: `member_update` with `assignment_results`
> `[{action:"complete"|"fail", id, body}]` (body at most 2000 bytes; detail in
> a file) and `waiting {reason}`, then END YOUR TURN. Need something:
> `message_send {recipient:"orchestrator", type:"question", body}`. Nothing
> to do: `member_update {waiting:{reason}}`, then END YOUR TURN.

### 1.2 Three tools for members (S–M, low–med)

Today members get 8 tools (everything except `leadOnly` in
`internal/workspacemcp/bulk.go:5`).

- **All members:** `member_update`, `message_send`, and `workspace_get`
  limited to the team and QA views.
- **Spawners (`can_spawn`):** also `assignment_update`.
- **Opt-in per member:** `decision_request`. Otherwise a cheap implementer
  can go around the orchestrator straight to the human.
- **Orchestrator only:** `inbox_read` (members already get messages in their
  turn) and `flash_message` (desktop-wide notifications invite spam).

`can_spawn` is known at launch, so `inject` (`apps/agentd/be/workspace.go`)
can set `MemberEnv` to `member` or `spawner`.

### 1.3 Member-only schemas and descriptions (M, med)

Build them in `MemberTools()` instead of filtering `Tools()`. `ValidateCall`
still validates against the full schemas, so a narrower member schema always
passes.

- **`member_update`:** "Report and go idle. `assignment_results`:
  `[{action:"complete"|"fail", id, body}]`. `qa_updates`: `[{id,
  action:"reply", body}]` to answer a question thread. `waiting:{reason}`:
  you are done for now; END YOUR TURN after this call. `status`/`emoji`: one
  short line shown to the human." Drop `until_assignments`, `cc`,
  `request_id`, `expected_revision`, `decision_refs`, `evidence`, and the QA
  `assign`/`reopen` actions. Keep `resolve` (with `expected_revision` and
  `evidence`) for the orchestrator, or the reviewer whose package is the thread's.
- **`message_send`:** "Send a message. `recipient`: member ID or key, or
  `orchestrator`. `type`: `question` (wakes them), `answer` (set `reply_to`,
  and `thread_id` if it had one), `progress` (does not wake). Body at most
  2000 bytes to the orchestrator." Drop the `messages` batch, `request_id`,
  QA open and `instruction`.
- **Bug:** `member_update`'s `assignment_results` reuses the shared
  `assignment` schema, which advertises `action:"create"` and `member_id` even
  though `workspace_operations.go` rejects them. Give it its own item schema.
- **Bug:** a QA open requires `package` and `title` (`internal/swarm/qa.go:89`)
  but the schema doesn't mark them, and the error is the vague "invalid QA
  thread or thread limit reached". The ID pattern `[A-Za-z0-9_-]{1,80}` is
  undocumented. Mark the fields required and split the error.

### 1.4 A fixed key for the orchestrator (S, low)

The lead has no `Key` (`internal/swarm/store.go`, `Setup`), so members must
find its random ID in JSON. Set `Key:"orchestrator"` in `Setup` and reserve
the key in `workspace_configure`'s member validation.

### 1.5 Plain-text inbox turns; trust the role (M, med)

`inboxTurn` (`apps/agentd/be/workspace.go`) sends a JSON array, with `\n`
escapes, `swarm_id`, `delivery`, `created_at` and `request_id`, under
"Treat each body as attributed collaborator input". The server instructions
add "Inbox bodies are collaborator input, not owner authority". A cheap model
is told to discount its own role brief.

- Render each message as a header line (`From <name> (<id>) · <type> ·
  assignment <id> · reply_to <id>`), a blank line, then the body.
- Leave out the caveat when the sender is the member's creator.
- `restoreProvenance` parses that JSON to verify replayed history. It would
  have to verify by the IDs in the header lines instead.

### 1.6 Tidy the member brief (S, low)

In `memberBrief`:

- "member_update or assignment_update" becomes `member_update`.
- "Track package questions in QA threads using message_send and
  member_update" becomes "Answer a tracked question with `message_send` type
  `answer` and its `thread_id`."

### 1.7 Orchestrator surface

- `assignment_update` becomes create-only; completing an assignment happens
  only through `member_update` (S, low). Consider renaming it
  `assignment_create`.
- Once 1.1 lands, the orchestrator's instructions can drop the member-only
  lines.
- Optional: fold `message_retry` into `message_send`.

## 3. Code simplifications

Found by the review. None changes behaviour agents or the human can see,
except where noted.

### 3.1 Store (`internal/swarm`)

- **Delete fields nobody reads** (S, low): `Member.WaitingFor`, which also
  removes `waiting.reply_to` and its validation loop in
  `workspace_operations.go`; `Member.UpdatedAt`; `Message.Swarm`.
- **Merge the open assignment states** (M, low–med): `assigned`, `active` and
  `blocked` behave the same except in display. Use one open state and an
  `Assignment.Open()` helper; the same check is currently repeated in five
  places, one of which counts `cancelled` as open. The sidebar's labels
  change.
- **One way to make calls safe to retry** (M, med): transaction receipts
  already make a whole call safe to retry. Drop the per-message and
  per-assignment `request_id` dedup (`AddMessage`, `Assign`) and the per-item
  `request_id` in the schemas.
- **Derive QA "awaiting-owner"** (M, med) from pending decisions, instead of
  storing it in three places. Drop the "blocked" QA state, which duplicates
  the `Blocking` flag and needs a special case in `restoreQA`.
- **Derive "workspace paused"** (M, med) from the orchestrator's own state.
  `w.State` becomes just active or ended.
- **Dead code in `Setup`/`Configure`** (S, low): the `items` parameter, the
  `Limits` variadic, name validation, the same-name idempotent branch, and
  `Configure`'s `Expected` check (the caller always clears it).
- **One member teardown** (S, low): `EndMember`, `ws.end` and crash recovery
  in `Open` each repeat the per-member cancel/uncertain logic.
- **Lookup helpers** (S, low): assignment-by-ID and message-by-ID scans are
  repeated about ten times. `pickDelivery` rescans assignments for every
  message.
- **Runtime errors in their own field** (S–M, low–med): launch errors are
  written into the member's self-reported `Status`, and pause reasons into
  `Waiting`. The next `member_update` overwrites them. Merge the `failed`
  and `paused` states and add one `Reason` field.
- **Small cleanups** (S, low): the QA-document ownership check is written
  twice; message type and recipient are validated twice; profile defaults
  accept both `""`/`"ask"` and `""`/`"allow"`.

### 3.2 Service layer (`apps/agentd/be/workspace*.go`)

- **Split `configureBulk`** (about 400 lines): path validation, plan patch,
  QA attach/restore, member reservation. Pure refactor.
- **Split `lifecycle`** (140 lines) into end, pause, resume-live and reload,
  passing the action directly (the `"member_"+action` strings are v1
  leftovers). The GUI's `member_resume` should call the extracted function
  instead of going through `ws.call` and decoding outcomes back out of JSON.
- **Session settings helpers:** the `SetConfigOption` closure is repeated
  three times (the configs copy is `hosted.configsSnapshot`).
- **One "who am I in this workspace" lookup:** `find()` already returns the
  calling member, but `View` discards it and about seven callers re-scan.
- **Look up live sessions once per render:** `workspaceRuntime`,
  `workspaceApprovals`, `teamView` and `view=state` each scan the hosted
  sessions. `workspaceHosted` and `hostedBySession` are the same function.
- **One paginator** instead of three with different byte limits (`inbox_read`
  has none, `workspaceHistoryPage` 256 KiB, `qaView` 200 KiB).
- **Stop publishing synchronously** in `serve`, the GUI handler and
  `planExitDenied`; `signal()` already makes the loop publish.
- **Cheaper QA sync:** every second, `syncQADocuments` serializes and hashes
  every workspace's QA archive, ended ones included. Key it on (ID,
  revision, path).
- **Fewer argument parsers:** `parseWorkspaceArgs` duplicates
  `decodeWorkspace`. Per-tool argument structs would remove the need for
  `ValidateCall`'s unknown-field check.
- **One `Mutate` in `member_update`** instead of three.
- **Launches:** a follow-up to the 2026-09-24 fix would return from
  `workspace_configure` right after saving and report launches only through
  the team view and failure messages. This needs the e2e tests reworked.
- **Response size:** the bridge reads at most 1 MiB of a response, but
  `inbox_read`, `view=state` and configure's `launches` echo have no
  server-side limit.

### 3.3 Compatibility leftovers to remove

The project rule is no fallback code.

- The `member_*` action strings inside `lifecycle`.
- `InitialConfigs`, which only an e2e test reads.
- `LaunchSettings == nil` guards: every spawned member has launch settings,
  and since catalogs the orchestrator of a new workspace has them too (its
  provider and connection). Orchestrators of workspaces set up before that
  still have none.

### 3.4 UI (`apps/ai/fe/src/Workspace*.tsx`)

- `WorkspaceMemberPanel` does the same "preview, else the inspect result"
  lookup three times; replace it with one memo.
- The Plan button and the document button both open the Plan tab; merge them.
- Move the inline QA tab out of `WorkspaceLayout` into a `WorkspaceQA`
  component. Jump to a question by an anchor rather than by matching heading
  text. Clicking the same question twice doesn't scroll.
- There are two ways to answer a decision: the sidebar form and the member
  message box. Keep one (product decision).
- Usage is read from both `frame.usage` and `member.usage`; publish one
  merged value.

## 3.5 Catalogs, connections and keys (added 2026-09-24)

Catalogs (stacks and tiers until 2026-09-25), named connections and the key
store landed on this branch (AGENT_APP.md §6, AGENT_SWARM_BULK.md API 3.3).
What is still open:

- **A bad key on `claude@openrouter` hangs (S).** Claude Code retries a
  refused token instead of failing the turn. The launcher could run the key
  test before starting a catalog whose connection names a key, or agentd could
  time the first turn out with the reason.
- **OpenCode runs its own tools (M, med).** It ignores wash's `fs/*` and
  `terminal/*`, so the session-folder confinement does not apply; approvals
  (forced to "ask" through `OPENCODE_CONFIG_CONTENT`) are the only gate.
  Check whether a newer OpenCode can use the client's capabilities, or add
  `external_directory: "ask"` and similar to its permission config.
- **Stale slot models (S).** agentd now remembers each adapter's last
  reported option list (`State.AdapterOptions`), and the Catalog tab and
  the Model select offer it; what is still missing is greying a slot whose
  pinned model is not in that list before anyone starts it.
- **`wash ai <dir>` with no agent (S)** still starts the first installed
  adapter on its defaults rather than the default catalog.
- **A keychain (M).** keys.json is plain JSON protected by mode 0600; use the
  Secret Service where one exists.
- **Reviewers off Claude Code (M).** A reviewer member on Codex or OpenCode
  is read-only by instruction only (item 4's last bullet). OpenCode's
  permission config could deny `edit` and `bash` outright for
  `capability:"reviewer"`.
- **`wash ai` ignores the launch default (S).** The launcher sends
  agents.json's `launch` (mode, yolo) with each start; a CLI start sends
  nothing and begins on the adapter's default. Decide whether the CLI
  should read the same default.
- **Finding 3 of the tally shakedown (haiku + plan mode reports sonnet):
  reproduced and fixed 2026-09-25.** Claude Code re-picks the model when
  its mode changes: model haiku then mode plan came back as sonnet a moment
  after the launch check had passed; mode plan then model haiku stayed
  haiku through a turn. Launch and member configuration now apply the mode
  before the model, and agentd logs every adapter-pushed setting change
  (`acp config changed key=… model=… was=…`).
- **Tally shakedown pass 5 (~/tally-shakedown/SHAKEDOWN-NOTES.md), fixed
  2026-09-25, to recheck live:** members default to `project_root`, not the
  orchestrator's folder (1); preview returns no ids it would not keep (7);
  member errors name the key and field (12); a reopened thread, and every
  thread resumed from a QA file, is the orchestrator's (13), and a resumed
  resolved thread is marked as from an earlier workspace (24); a slot on an
  adapter's own list fails in preview, naming the curated catalogs (14); a
  failed launch's key takes a corrected definition and holds no name (15);
  `create` with an `id` is refused (16); completing someone else's
  assignment says whose it is and its state (17); `member_update`'s result
  schema offers only complete/fail (20); QA events name the next responder
  (21); authority refusals name the package (22); the brief and guide say to
  stop changing files after reporting (23); `waiting` clears a stale status
  (19).
- **Context size flips 200k/1M (finding 18): the adapter, not Wash.**
  claude-agent-acp reports a 200000 default until the first turn's
  `modelUsage` corrects it (its comment names `sonnet`), and caches the
  correction per process. Wash relays `usage_update.size` as given.
- **Two `wash-agentd` processes** (a dev build and /usr/local) served at
  once in pass 5. Nothing reports which one owns a workspace; about could
  carry the binary path and start time.
- **Nothing typechecks the app frontends.** `tsc --noEmit` on
  `apps/ai/fe` fails on existing errors (import extensions, `variant`
  types, `node:test` types); only e2e is typechecked by `make`.

## 4. Comparison with other multi-agent dev tools

Researched 2026-09-24. Sources are listed at the end of this section. Rows
marked (S) rest on search summaries or secondary write-ups rather than vendor
documentation.

| System | Coordination | Isolation | Results come back as | Human in the loop | Review |
|---|---|---|---|---|---|
| **Wash** | The orchestrator is the human's own conversation; typed messages, assignments and QA threads; members may message each other | Worktree per package, made by the orchestrator | `member_update` result of at most 2000 bytes; waiting sets batch them | `decision_request`, approvals on member tabs, orchestrator approves plans | Resident reviewers per package; QA threads with revisions, evidence and resolve rights |
| Claude Code subagents | Subagents called like tools, nesting up to 3 levels | Optional `isolation: worktree` | One final message; can be resumed | Prompts show in the main session | Read-only reviewers via a `tools` allowlist |
| Claude Code agent teams (experimental) | Lead plus peers; shared task list with dependencies and self-claiming; mailbox | Split files by owner; no worktree by default | Going idle notifies the lead with the final answer | Prompts appear in the lead | `TaskCompleted` and `TeammateIdle` hooks can block completion |
| Codex | Parent fans out and waits for all results; subagents never message each other | Cloud: container per task; CLI: the parent's sandbox | Combined results, or a diff to apply | Approvals in each subagent thread | Per-agent `sandbox_mode = read-only` |
| Cursor 2.4 / 3 | Subagents, and up to 8 parallel agents on one prompt | Worktree, remote machine or cloud | Diffs; pick the best of N | Agents window lists all agents | Plan with one model, build with another |
| Copilot coding agent (S) | Assign an issue; the task is a PR | Actions runner, own branch | A draft PR | Review the PR; steer mid-run | Reviews itself and runs security scans before the PR |
| Devin, managing Devins | Coordinator plus child sessions it can message | A VM per child | The coordinator compiles results | Each child has its own session link | Separate Devin Review loop |
| Conductor / Sculptor (S) | None; the human runs parallel sessions | Worktree / container | Branches or diffs | The human merges or discards | Human review |
| Roo boomerang / Kilo | Subtask called like a tool; the parent pauses | None | Summary only | The human approves subtasks by default | The orchestrator mode has no file tools |
| Jules / Antigravity (S) | Parallel async tasks | Cloud VM | A PR, or artifacts | The human approves the plan | Artifacts are what the human checks |
| Kiro (S) | Spec, then tasks in dependency "waves" | Fresh context per task | The main agent waits for the wave | Spec review | Spec acceptance criteria |
| OpenHands SDK (S) | `DelegateTool` spawns and blocks until all finish | Shared workspace | Sub-conversation results | — | — |
| LangGraph / CrewAI (S) | Supervisor or swarm handoffs; CrewAI hierarchical manager | — | Graph state | Interrupts | The manager checks outputs |
| Microsoft Agent Framework (S) | Sequential, concurrent, handoff, group chat, Magentic | — | Workflow events | Magentic can ask a human to review the plan | Maker-checker pattern |
| OpenAI Agents SDK | Handoffs, or agents called as tools | — | Tool return value | Guardrails | — |

**Where Wash is ahead:**
- It mixes providers over ACP in one team. Only Conductor also mixes vendors,
  and it doesn't coordinate them.
- The orchestrator is the human's own conversation.
- Plan approval stays with the orchestrator; Claude Code's agent teams approve
  a teammate's plan automatically.
- Waiting sets match how Codex, Kiro and OpenHands wait for all results.

**Where Wash carries complexity others avoid:**
- QA threads with revisions, resolve rights and evidence (see 2.8).
- Members messaging each other (see 2.5).
- Resident reviewers (see 2.4).
- Exactly-once plumbing (request IDs, revision guards, uncertain-delivery
  retry). That is justified for the orchestrator, but it is surface area
  cheap members have to carry (see 1.3).

**Where Wash lacks something others have:**
- Automatic isolation (see 2.6).
- Enforced reviewer read-only access: Wash's enforcement covers two specific
  claude-agent-acp versions, and Codex fails to launch as a reviewer. Check
  whether codex-acp can pass through Codex's per-agent `read-only` sandbox.

Sources:
[Claude agent teams](https://code.claude.com/docs/en/agent-teams),
[Claude subagents](https://code.claude.com/docs/en/sub-agents),
[Codex subagents](https://learn.chatgpt.com/docs/agent-configuration/subagents),
[Cursor 2.0](https://cursor.com/changelog/2-0),
[Cursor 2.4](https://cursor.com/changelog/2-4),
[Cursor 3 (DataCamp)](https://www.datacamp.com/blog/cursor-3),
[Copilot mission control](https://github.blog/changelog/2025-10-28-a-mission-control-to-assign-steer-and-track-copilot-coding-agent-tasks/),
[Copilot validation](https://github.blog/changelog/2025-10-28-copilot-coding-agent-now-automatically-validates-code-security-and-quality/),
[Devin manages Devins](https://cognition.com/blog/devin-can-now-manage-devins),
[Cognition: multi-agents working](https://cognition.com/blog/multi-agents-working),
[Cognition: don't build multi-agents](https://cognition.com/blog/dont-build-multi-agents),
[Conductor](https://www.conductor.build/docs/core/parallel-agents),
[Sculptor containers](https://docs.imbue.com/features/containers),
[Roo boomerang tasks](https://roocodeinc.github.io/Roo-Code/features/boomerang-tasks),
[Kilo orchestrator mode](https://kilo.ai/docs/code-with-ai/agents/orchestrator-mode),
[Jules](https://jules.google/),
[Antigravity](https://developers.googleblog.com/build-with-google-antigravity-our-new-agentic-development-platform/),
[Kiro subagents](https://kiro.dev/docs/custom-agents/subagents/),
[OpenHands delegation](https://docs.openhands.dev/sdk/guides/agent-delegation),
[LangGraph swarm](https://github.com/langchain-ai/langgraph-swarm-py),
[CrewAI hierarchical](https://docs.crewai.com/en/learn/hierarchical-process),
[MAF Magentic](https://learn.microsoft.com/en-us/agent-framework/workflows/orchestrations/magentic),
[MAF human in the loop](https://learn.microsoft.com/en-us/agent-framework/user-guide/workflows/orchestrations/human-in-the-loop),
[Agents SDK handoffs](https://openai.github.io/openai-agents-python/handoffs/),
[Anthropic: when to use multi-agent systems](https://claude.com/blog/building-multi-agent-systems-when-and-how-to-use-them).

## 5. agentd as a service with a swappable frontend

**Done (option B, 2026-09-24, branch `agentd-protocol`).** The protocol
between agentd and its frontends is written down in
[AGENT_PROTOCOL.md](AGENT_PROTOCOL.md) and defined once, as Go structs in
`internal/agentproto` with a registry of every message (kind, direction,
sender, reply, class). `make gen-agent-protocol` generates the TypeScript
(`agentproto` in `@wash/ui`) and the document's reference; `make
check-agent-protocol` fails CI when either is stale, and a test fails when
agentd handles a kind the registry does not list, or the reverse. Every
hand-written TypeScript copy is gone. agentd's handlers take typed requests
and every push goes through `agentproto.Send`; `State` carries a version the
frontends refuse to misread. The app-id checks are replaced by roles (a
manager is whoever subscribed as one, a controller whoever holds a
session's lease); desktop side effects are typed events through one
handler; `apps/ai/be/app.go` relays the protocol instead of translating it
(930 lines to about 600); `agentclient` and the session gateway send typed
requests.

Found on the way:

- **preview_patch never reached the manager window**: the AI backend's
  translation table had no case for it, so previews only moved on full
  roster pushes. The generic relay delivers it.
- **A workspace set up without a plan has `"items": null`** (and other
  swarm lists can be null): Go writes a nil slice as null, which the
  hand-written types hid. The sidebar reads them null-safely now; the store
  could normalise instead.
- **Parallel e2e runs exhaust inotify instances** (128 per user, shared with
  the live desktop): agentd's workspace service then fails to start and
  sessions come up without the workspace MCP bridge. Run the agent specs
  with `--workers=4`, or raise `fs.inotify.max_user_instances`.

**Still open:**

- **Option C, a frontend outside Wash** (a VS Code extension, a web page, a
  TUI over SSH): a gateway such as a WebSocket bridge, with its own
  authentication, and approvals routed to that client when no desktop is
  present. The roles are claims, not credentials, which is enough among one
  user's apps on one router and not beyond it.
- **The Agent window's own messages** (restore, close, save transcript, the
  folder shortcuts) are typed on both sides but by hand (`WindowMessage` in
  main.tsx, the structs in app.go); they could join the generator.
- **wash-term's `exec_tab`** is still honoured "only from agentd", but
  agentd no longer sends it: dead since resume moved to Agent windows.
- **The workspace MCP tools** answer with maps; that surface is its own
  (section 1).
