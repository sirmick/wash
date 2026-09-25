# Agent workspace backlog

Status: started 2026-09-24 on the `workspace-approvals-debug` branch, after a
review of the whole workspace feature. The design is in
[AGENT_SWARM.md](AGENT_SWARM.md) and [AGENT_SWARM_BULK.md](AGENT_SWARM_BULK.md).
This file lists what is still to do and the decisions behind it. When an item
is done, delete it here and record the change in its commit message.

The working assumption behind most items: **the orchestrator is a top-tier
model working with the human, and members (implementer, simplifier,
reviewers, red team) run on cheap models.** Anything the orchestrator reads
can be dense. Anything a member reads must be short, unambiguous and hard to
misuse.

Each item has a size and a risk (S/M/L, low/med/high) and the files it
touches. Line numbers are as of 2026-09-24.

## 0. Done in the 2026-09-24 pass

For context only; the commits have the detail.

- A review round wakes the orchestrator once. Answers and progress from
  members of a waiting set are held with the set's results. Everything
  queued goes out in one turn, and a member's turn carries at most one
  instruction or question.
- A relative `document.path` resolves under the project root. A `create`
  sent to `member_update` now says to use `assignment_update`.
- Ten bugs found by the review: cancelled assignments settle a waiting set;
  launches outlive the configure request; Stop only pauses a member with a
  live turn; `member_control` is orchestrator-only; the revision counts
  configuration changes only; request receipts are scoped to their
  workspace; relaunch no longer unpauses the workspace; relaunch keeps the
  orchestrator's setting changes; an owner decision shows once; an ended
  member's decision reopens its QA thread.

Still to check in a live workspace: all of the above.

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

## 2. Workflow changes (product decisions)

These change how a workspace runs, not just what agents read. Each needs a
yes or no from the owner first.

| # | Idea | Why | Size |
|---|---|---|---|
| 2.1 | **Checks run by Wash.** An assignment carries a `check` command, such as `go test ./pkg/timer`. Wash runs it when the member reports completion; if it fails, the output goes back to the member, not up to the orchestrator. | Moves gates out of prompts, which cheap models forget, and into code. Claude Code's `TaskCompleted` hook and Copilot's checks before a PR work the same way. AGENT_SWARM.md §2 currently leaves gates to instructions. | M |
| 2.2 | **Idle nudge.** A member whose turn ends with an open assignment and no report gets one automatic reminder. | Stops a forgetful model stranding a task. Like Claude Code's `TeammateIdle` hook. | S |
| 2.3 | **Structured results.** `{status, files, test_command, exit_code, open_questions}` instead of 2000 bytes of free text. | Easier for a weak model to write and for the orchestrator to check. Like the Agents SDK's typed handoff input. | S–M |
| 2.4 | **A fresh reviewer each round.** Ephemeral reviewers with explicit criteria, replacing resident reviewers. | Cognition and Anthropic both found a reviewer does better without the writer's context. Also drops the "refresh the diff" instruction. | S (mostly docs and examples) |
| 2.5 | **Route everything through the orchestrator.** No member-to-member messages, apart from an optional "ask the architect". | Codex doesn't let subagents talk to each other; Cognition found agent-to-agent negotiation "mostly a distraction". | S |
| 2.6 | **Automatic worktree per package.** | Cursor, Conductor and Claude Code's `isolation: worktree` do it; in Wash the orchestrator has to. | M |
| 2.7 | **Best of N** for risky packages: 2–3 cheap implementers, the orchestrator picks one diff. | Cursor runs up to 8 in parallel. Often beats one cheap attempt followed by review loops. | M |
| 2.8 | **Shrink QA to questions with a thread ID.** Drop the separate thread state machine and generate the Markdown from messages. | No other tool has revisioned QA threads; it is a second state machine beside assignments. | L |
| 2.9 | **Result = final message.** A member's last text in the turn becomes its result, instead of a `member_update` call. | Claude Code subagents, Roo and Codex all work this way. Risky, since a turn can end for other reasons; 2.2 is the safer first step. | M |

Suggested order: 2.2 and 2.1 first, then 2.3 and 2.4; decide 2.5 alongside
1.2.

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
- **Session settings helpers:** three copies of "copy configs under
  `hostedMu`" plus the `SetConfigOption` closure.
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
