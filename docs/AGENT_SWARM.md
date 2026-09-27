# Agent swarms through MCP

Status: original implementation/design, 2026-09-22; the plan graph replaced the
keyed progress list and the registered document on 2026-09-26 (API 4). The
implemented bulk interface, the plan tools and first-class QA are specified in
[AGENT_SWARM_BULK.md](AGENT_SWARM_BULK.md); it defines the only supported tool
surface. Removed tools are rejected.

See the [validation notes](AGENT_SWARM_VALIDATION.md) for what was run while
building this, and the [Redoubt project example](examples/redoubt-workspace.md)
for the worked setup it was designed against. Open work, the comparison with
other multi-agent tools and the refactor options are in the
[backlog](AGENT_SWARM_BACKLOG.md).

## 1. First use case

Run Redoubt's development workflow from an existing Wash Agent conversation:

> Read this project's swarm instructions, configure the team, register the
> plan, and start the ready work.

Until setup, this is an ordinary Agent window with its existing conversation
layout. The agent reads the requested project file(s) and calls `workspace_configure`.
That successful call attaches the workspace and makes the right-hand sidebar
appear in the same window, preserving its conversation and composer.

The current agent becomes the orchestrator. It keeps an architect resident,
launches implementers for packages, creates temporary reviewers, receives their
messages and results, and keeps the plan: a graph of nodes that every
assignment and member hangs off. The user sees the team in a right-hand column
and the plan in a Plan tab beside the conversation.

The reference is `/data/redoubt/docs/SWARM.md`, `.pi/agents/`, and
`.pi/workflows/`. These supply the example workflow, not executable instructions
for building this feature.

## 2. Agreed boundary

Wash exposes workspace and collaboration primitives over MCP. Agents read the
project's JSON, TOML, Markdown, or other instructions and configure the workspace
through those tools. Wash does not need a project workflow parser in this version.

`workspace_configure` receives concrete settings derived by the agent: a name,
project root, the QA directory and plan file, and optional concurrency/member
limits. The plan itself is `plan_set`'s. Source-file
references remain in project instructions and explicit member role messages;
Wash does not interpret their format. `workspace_get` returns the compact team view by default; `view:"state"` returns the workspace as JSON, including configuration, revisions,
the catalog, members, launch snapshots, plan, assignments, pending decisions, delivery
counts, and live sessions' adapter options. `workspace_configure` changes the
name, concurrency/member limits and catalog atomically. Further MCP calls
populate members and assignments.
Reading a project file alone does not activate workspace UI.

`workspace_end` reverses setup: end the workspace's child sessions, retain
history, detach workspace state, and remove the sidebar. The top-level session
continues as an ordinary Agent conversation and can set up another workspace later.

The orchestrator decides the plan's shape, allocates worktrees using its
ordinary tools, coordinates reviews, decides acceptance, integrates changes, and
maintains the plan. Wash owns membership, session lifetimes, message delivery,
runtime state, the GUI, and the plan's truth: every assignment is on a node,
work starts only when the node's needs are done (or the start records why not),
and only the orchestrator sets a node done. A workflow instruction beyond that is
not a runtime-enforced gate unless a corresponding primitive implements it.

Resident and ephemeral describe lifetime, not speed: an ephemeral implementer
may work for hours and wait for several replies before its assignment completes.

## 3. Existing implementation to extend

| Concern | Current implementation |
| --- | --- |
| Agent window | `apps/ai/be/app.go`, `apps/ai/fe/src/main.tsx`; one controlling window per session |
| Agents manager | `com.wash.agents`, using the same bundle; launcher/history on the left, running sessions on the right |
| Hosted sessions and turns | `apps/agentd/be/acp.go`, `adapters.go` |
| Controller ownership | `apps/agentd/be/controller.go`; transcript subscribers are separate from the controller |
| MCP configuration | `internal/agentpolicy/agentpolicy.go`; stdio server definitions passed to ACP session creation/loading |
| Transcript rendering | `web/lib/src/agent-session.tsx`; data and callbacks supplied by its host |
| Transcript persistence | `apps/agentd/be/transcript_store.go` |
| Multiple keyed subscriptions | `internal/agentclient/agentclient.go` |
| Markdown and file updates | Shared Markdown renderer; existing filesystem watch/read facilities used by wash-edit |

Hosted sessions already run independently of windows. Existing prompt turns are
serialized, but pending prompts are in memory and are discarded on cancellation
or failure. Collaboration delivery needs its own retained inbox and provenance.
Existing `agent_draft` messages insert text into a human composer; they are not
the agent-to-agent transport.

## 4. Runtime model

A swarm has a stable ID, project root, name, orchestrator member ID, a plan
(nodes), members, assignments, QA threads and messages.
Initially a session belongs to at most
one active swarm. Multiple independent swarms may exist on a host.

A member has a stable ID, display name/role, creator, provider, initial
instructions, working directory, lifetime, and a binding to a hosted session.
Role names describe responsibilities; they do not grant permissions by themselves.
Transient `acp:<n>` roster keys must not serve as durable member IDs.

An assignment has an ID, assigner, assignee, plan node, description, state,
and optional result. It is a correlation and completion record on a node; the
node's needs gate its start, and its result moves the node to reported. The
orchestrator decides which assignment starts next.

Separate these dimensions:

- Member lifecycle: starting, available, paused, failed, ended.
- Turn activity: idle or working, derived from the hosted session.
- Waiting reason: instructions, reply to a message, or owner decision.
- Assignment state: assigned, active, blocked, completed, failed, cancelled.
- Self-reported status: a short text, optional emoji, and update timestamp,
  supplied by the member through MCP and displayed in the sidebar.

`member_update({status, emoji})` replaces the caller's own display status;
empty text and emoji clear it. The caller is identified by its session credentials,
so no target member ID is needed. Examples: `🔎 Reviewing timer behavior`,
`🛠️ Implementing K5`, and `⏳ Waiting for architect`. Validate bounded plain text
and support composed emoji. Publish changes immediately to subscribed views and
retain the latest value across GUI reconnects. Status updates do not send inbox
messages or wake other members.

Self-reported status describes the work; lifecycle and turn activity remain
runtime-owned. Setting `✅ Done` does not complete an assignment, and a paused,
failed, or ended member must still visibly show that runtime state alongside its
last reported status.

A normal turn ending makes a member idle. It does not complete an assignment or
terminate the session. A resident stays available after reporting a result. An
ephemeral member ends after explicitly completing its assignment and reaching a
turn boundary. Archive its transcript, messages, and result before retirement.

## 5. Typed inboxes

All collaboration messages use one envelope:

`id, swarm_id, sender, recipient, type, assignment_id?, reply_to?, body, created_at`

Wash stamps sender identity and timestamps. IDs returned by tools are suitable
for reply correlation and retry deduplication. Message bodies may contain text
and references to project files; do not copy entire transcripts into each message.

Initial types:

| Type | Purpose | Behavior |
| --- | --- | --- |
| `instruction` | Assign or redirect work | Delivered as actionable input; assignment creation uses the assignment primitive |
| `question` | Ask a member for a concrete answer | Reply uses the question's message ID |
| `answer` | Respond to a question | May unblock the recipient's waiting state |
| `progress` | Report an intermediate update | Visible in the GUI; does not wake an idle recipient by default |
| `result` | Explicit assignment outcome | Created by completion/failure operations and wakes the assigner |
| `decision_request` | Ask the human for a choice | Appears in the GUI with recommendation/options; unrelated work continues |
| `decision_response` | Record the human's answer | Attributed to the human and delivered to the requesting member |
| `lifecycle` | Member failed or ended | Runtime-generated; failures notify the orchestrator; readiness is visible in the member roster |

The body of an ordinary message cannot impersonate a runtime result or a human
decision. Lifecycle and decision messages use their owning operations. Exact tool
names and schemas below are provisional; these distinctions are the contract.

### Delivery and waiting

1. Persist an accepted message before reporting successful send.
2. A single per-session dispatcher serializes human prompts and actionable inbox
   deliveries; never issue overlapping ACP prompts.
3. An idle recipient starts a turn for actionable input. A busy recipient receives
   it after its current turn. Preserve per-recipient order; bounded batches are
   allowed if every included message ID remains explicit.
4. A paused or failed member retains pending messages. Stopping a turn pauses
   automatic inbox dispatch, so queued mail cannot immediately undo the user's
   Stop action. Resume is explicit.
5. `member_update.waiting` records the reason and optional correlation ID, returns promptly,
   and instructs the agent to finish its turn. The agent is only actually waiting
   once the turn ends. Recheck the inbox then: a reply arriving before the yield
   must not be lost or stranded.
6. Delivered messages enter the transcript with real sender/type provenance. Do
   not render them as text the human typed. Receiving agents also see that origin.

Waiting consumes no polling turns and holds no blocking MCP call open. A resident
session can remain available with no assignment. Progress events are available
through status/inbox reads and accompany the next actionable delivery.

Track accepted, queued, dispatched, and delivered separately. A message is
delivered when the turn that carried it ends cleanly; there is no separate
acknowledgment, which cost every member one more full-context request per
wake-up and proved nothing the turn's end did not. Delivery does not prove the
requested work happened. If a crash leaves dispatch uncertain, show
that uncertainty and retain the message. Do not promise exactly-once execution or
silently repeat a potentially mutating assignment.

## 6. MCP tool surface and injection

One service in agentd implements the operations. The GUI calls the same service
through Wash app messages. A bundled stdio MCP bridge exposes it to hosted agents.
Do not add a separate workflow daemon or make MCP bridge processes own the swarm.

Exactly eleven operations are advertised and accepted; removed v1 names fail with
Unknown tool. There are no compatibility aliases. See the full schemas and examples
in [the bulk MCP contract](AGENT_SWARM_BULK.md).

| Tools | Purpose |
| --- | --- |
| `workspace_get`, `workspace_configure`, `workspace_end` | Discover/read, atomically configure, explicitly detach |
| `member_control`, `member_update` | Lifecycle controls and combined status/results/waiting/QA reporting |
| `message_send`, `inbox_read`, `message_retry` | Attributed messages and durable inbox delivery |
| `assignment_update` | Create assignments or report results |
| `decision_request`, `flash_message` | Human decisions and desktop notifications |


All new ordinary Wash-hosted sessions receive the bridge at session creation, so
they can later become orchestrators without changing their tool list. Child
sessions receive it too. Preserve configured user MCP servers and reject reserved
name collisions clearly. Existing sessions need an explicit rebind/resume path if
their adapter cannot add tools live; do not restart a busy session automatically.

Availability of MCP tools does not create a workspace or reveal a sidebar. Keep
the bridge available after teardown so the same conversation can invoke setup again.

The bridge uses a private, authenticated host-local endpoint into agentd. Each
launch receives session-scoped credentials; agentd derives the caller from those
credentials, not a caller-supplied member name. Supply credentials separately to
the MCP process, avoid logging them, and revoke them when the session ends.
The initial transport spike must determine the smallest reusable Wash IPC route.

All members can collaborate. The orchestrator can grant spawning capability to
members that need it. Bound membership/concurrency through explicit swarm
settings; children stay within the authority of the launching session. Human
permissions remain human permissions; an orchestrator cannot fabricate approvals.

### Catalogs and state readback

A member's model comes from a catalog (AGENT_APP.md, "Catalogs, connections
and keys"): the workspace's, which is the orchestrator's own unless
`workspace_configure.catalog` names another, or one the member names itself
with `catalog`. `members[key].model` is a slot of a curated catalog
(`frontier`, `coding`, `small`; frontier when omitted) or a model id the
catalog's adapter offers. Model IDs are opaque provider values; Wash never
translates a marketing name into a guessed ID. Read the actual IDs and
allowed values from `workspace_get({"view":"state"}).sessions[member_id].config_options`
for a live session of that provider, or prefer a slot where the catalog has
them. An adapter's own list (e.g. `anthropic`) has no slots, and a slot name
on it fails at configure time (preview included), naming the curated
catalogs for that adapter. Invalid model ids report the adapter's allowed
values at launch; the failed member's key then takes a corrected definition. The per-workspace `profiles` map and
`default_profile` are gone (2026-09-25): a curated catalog on the Catalog
tab does that job globally.

`workspace_configure` is orchestrator-only and persists with the workspace.
`expected_revision` refers to `workspace.revision` (not the plan revision); a
stale write fails atomically. Reducing `max_active` allows running turns to
finish and limits new dispatches. `max_members` cannot be reduced below
current membership. A catalog change affects later launches only.

Explicit `effort` and `configs` sit on top of the slot's; `configs` merge by
option ID. Conflicting semantic and raw values for the same setting are
rejected. A `provider` override must match the catalog's adapter.

Wash resolves the slot atomically while reserving membership, applies the
mode first, then the model, then validates effort against the refreshed
adapter choices. Unknown settings, invalid choices, RPC failures and silently
substituted values fail the launch before role instructions or tasks are
delivered. Model and effort use ACP categories (`model`, `thought_level`);
other settings use exact IDs.

Each child retains its catalog and model as asked, its resolved
`launch_settings` snapshot and the `initial_configs` returned by the adapter.
Editing a catalog changes future launches, never existing sessions. Member
details show the catalog, slot, provider, resolved model and effort. `workspace_get.sessions` reports
current options for live sessions, which can differ after GUI setting changes;
initial settings are deliberately retained as history. Paused/unloaded/ended
sessions can have no live entry. State and live provider options are separate
snapshots; the workspace revision only guards durable workspace mutations.

`workspace_get` returns `null` before setup and never activates the sidebar.
By default `workspace.messages` contains pending decisions;
`delivery_counts` summarizes all retained mail. `include_messages: true` returns
a history page in `workspace.messages`, with `message_page.cursor` and
`message_page.has_more`. Use `after` to continue, optionally `limit` (default 50,
maximum 100); pages are additionally bounded to 256 KiB of message JSON. This
keeps readback from replaying an entire long-running conversation. Individual
members can also use the existing paged `inbox_read` for their own mail.

## 7. Agent window and live plan

Preserve the current split between the Agents manager and individual Agent
windows. An ordinary Agent window has no workspace sidebar, empty placeholder, or
reserved sidebar space. Successful `workspace_configure` publishes the attachment
state that reveals a resizable right-hand column in the existing window. Failed
setup leaves the ordinary layout intact. Teardown removes the column and returns
the space to the conversation. A frontend reload reconstructs this choice from
agentd's attachment state; browser-local UI state does not activate a workspace.

Windows opened for members of an active workspace also receive its context. The
sidebar contains:

- Plan navigation: nodes done out of the total, opening the Plan tab.
- Members, grouped by plan node: name, role, lifetime, activity, waiting reason,
  and the member's own status text and emoji, refreshed live.
- Pending decisions and recent message/result activity.

### Flash messages

Any member can call `flash_message({text, emoji?, level?})` to bring a short
message to the human's attention. Show it through Wash's desktop notification
surface even when another app is focused, the swarm column is hidden, or the
Agent window is minimized or closed. Attribute it to the calling member and
swarm; derive identity from the MCP session. The default level is informational.

Reuse the existing notification service and keyed focus routing. Clicking the
notification opens/focuses the source member's Agent window and swarm context;
emitting it does not steal focus. Retain the message in swarm activity and the
notification history so the transient flash is not its only record. With no
connected desktop, retain it for later inspection; tool success means accepted
for notification, not proof that a human saw it. A flash does not pause work or
request a decision; `decision_request` handles questions requiring an answer.

Verify visibility with another app focused and with the Agent window closed,
correct source attribution, and click-through to the appropriate member.

### Member inspection and the Plan tab

The sidebar is navigation and status. The Plan tab stands beside Conversation
for the life of the workspace. Clicking a teammate opens a named tab containing assignments, transcript preview,
recovery controls, and a human inbox message field. Selecting the window's own
member returns to Conversation. Tabs are reused, closeable, and keyboard
navigable (arrow keys, Home/End, Delete to close).

The owning Conversation remains mounted while another tab is selected, preserving
its draft and attachments. Inbox drafts are kept separately per member until the
workspace changes. Only the selected member receives transcript updates. A new
workspace or teardown clears the extra tabs; reload starts at Conversation.

A draggable divider resizes the sidebar. A focused divider also accepts left/right
arrows and Home/End; the width preference is remembered locally without activating
a workspace. Both panes shrink within the window and scroll independently.

A transcript preview is a subscriber, not another controller. A swarm-scoped
subscription sends membership/message updates without subscribing every window
to the entire host roster. Fetch transcript history only for the selected member.

### The plan

The plan is a graph of nodes, barely structured: each has a stable `id`, a
one-line `title`, optional `emoji`, a `template` saying how it is drawn
(`milestone`, `package`, `step`, `note`), an optional `parent`, `needs` (node or
QA thread IDs done first), a short `body` (links to pages for detail), a `state`
(`todo`, `active`, `reported`, `done`, `failed`, or a word of the orchestrator's
own), a per-node revision, and recorded overrides. A milestone with nothing in it
is a sketch: three milestones, the first called "Plan", is a fine start, and the
orchestrator expands each as it is reached.

`plan_get` reads it (one line per node); `plan_set` upserts nodes by id in one
call and loads a plan file; `plan_accept` sets a node done and returns its merge
commit trailers. The Architect may plan nodes that have not started. Every change
is written to the plan file (`.wash/plan.toml` by convention), and a workspace
set up on that file resumes the plan.

The Plan tab draws it left to right: top-level nodes as columns in the order
their needs put them, each milestone's nodes in layers inside it, edges from
each need to what needs it, steps listed inside their package. A node shows its
state, its members with their live activity, and a "needs you" badge; a member
opens its tab, whose breadcrumb leads back. Changes reach the window as patches
of the changed nodes. See AGENT_SWARM_BULK.md for the tools and rules.

## 8. Lifetime, persistence, and recovery

Closing an Agent window leaves swarm execution in agentd. `workspace_end`
is a separate operation: stop accepting assignments/spawns, pause dispatch,
terminate child sessions, resolve pending assignments/decisions as cancelled,
retain history, and leave the top-level conversation available. Release workspace
subscriptions and remove the sidebar when teardown finishes.
Return the tool result to the still-running top-level session. Revoke child
credentials and remove the lead's workspace authority while retaining its MCP
bridge for future setup. Teardown preserves project files, the plan file, QA
thread files and worktrees; it uninstalls the active workspace from the
conversation. Ending with nodes still active needs `confirm:true`.

Setup and teardown retries must not create duplicate teams or double-stop members.
An active workspace must be explicitly torn down before setting up a different one
on the same session. Its archived history remains inspectable without reactivating
it or respawning agents.

An orchestrator may explicitly end members sooner. An orchestrator failure pauses
new automatic dispatch across the swarm after current turns settle and surfaces
the failure; it must not silently delete the team.

Persist swarm/member/assignment identities, the plan and its revisions,
messages, and delivery transitions in host state alongside existing agent
history. The plan file and the QA thread files carry the plan and the record of
why into the project, where a later workspace resumes them.
Do not store bridge credentials
in the project or transcript.

Initial restart behavior: restore the workspace as paused with interrupted members
identified. Resume sessions only through supported adapter loading, issue fresh
credentials, and reconcile uncertain deliveries before continuing. Unsupported
session restoration is visible and requires an explicit replacement member.
Automatic crash recovery and retry of work are later extensions.

## 9. Redoubt acceptance scenario

1. An ordinary Agent window initially shows only its existing conversation UI.
   Its agent reads Redoubt's instructions and invokes `workspace_configure`; the
   sidebar appears in that same window without replacing the session.
2. It plans three milestones with plan_set, the first called Plan, and creates
   one resident architect; reaching Build, it expands it into packages.
3. It launches two ephemeral implementers in already-allocated package worktrees.
4. One implementer asks the architect a question and yields; the other continues.
5. The architect answers in its existing session. The waiting implementer resumes.
6. A subsequent question reuses that architect and its conversation.
7. A genuine owner decision appears in the GUI; the response is delivered with
   human attribution while unrelated work remains possible.
8. Implementation results wake the orchestrator. It launches temporary reviewers
   against the relevant package checkout and receives their separate results.
9. After evaluating acceptance, the orchestrator accepts the package's node with
   plan_accept and puts the trailers on its merge commit; only that node changes
   in the Plan tab, and the plan is never retransmitted to the agent.
10. Reviewers retire after completion; the architect remains waiting. Reloading
    the browser or closing/reopening the window retains the running team.
11. On request, the orchestrator calls `workspace_end`. Children end, the
    sidebar disappears, and the same top-level conversation continues. Project
    files remain intact; a later setup works through the existing MCP connection.

## 10. Delivery sequence and verification

| Milestone | Deliverable | Evidence |
| --- | --- | --- |
| M0: tool transport | Bundled MCP bridge, session identity, merge into ACP new/load, one status operation | Fake adapter verifies injection; real Codex/Claude smoke sessions discover and call the same tool |
| M1: resident collaboration | Swarm/member creation, typed retained inbox, dispatcher, waiting and results | Two agents exchange multiple messages across idle periods; reply-before-yield, busy delivery, provenance, no overlapping prompts |
| M2: lifecycle and recovery | Assignments, ephemeral retirement, pause/end, workspace teardown, persisted state and explicit resume | Stop retains mail; result precedes retirement; teardown preserves lead/session/files and permits later setup; failure is visible; interrupted dispatch is not blindly replayed |
| M3: swarm column and plan | Setup/teardown-driven sidebar, keyed progress, optional live Markdown, member inspection, status/emoji, decisions, flash messages | Ordinary window before setup and after teardown; failed setup leaves layout intact; item patching preserves untouched fields/order; duplicate IDs and stale updates fail clearly; reload restores progress; status preserves runtime state; document edits/atomic replacement refresh; preview never steals controller; flash visible outside Agent window |
| M4: Redoubt walkthrough | End-to-end workflow above using instructions, without a workflow parser | Deterministic fake-adapter E2E plus a bounded real-provider exercise in isolated worktrees |

Use focused Go state/dispatcher tests, component tests for the new column, and the
existing fake-ACP E2E infrastructure. Do not start implementation by writing a
general workflow engine. Do not restart the developer's live Wash session to test.

### Protect the running desktop

- Before building, identify the live router's executable, asset roots, and control
  endpoint using read-only inspection. Work in an isolated checkout/build tree
  whenever the active desktop might load this checkout's generated assets or
  binaries. A build must not trigger live asset reload or replace an executable
  the running desktop uses to start its next app.
- Use `e2e/fixtures/router.ts`: it supplies a unique loopback port, private control
  socket, staged app binaries, and temporary XDG config/state directories. Give
  the new MCP endpoint the same per-test isolation. All automation must use the
  fixture's explicit endpoint, never a default live router socket or port.
- Do not run live service control, install/deploy targets, `make run`, `make dev`,
  or broad cleanup targets. In particular, the current Makefile has cleanup that
  removes `/tmp/wash-*`; it is unsuitable while the user's desktop is running.
- Stop only processes created by the current test harness, using captured process
  handles and verified per-test ownership. Never use `killall`, broad `pkill`, or
  a process-name match to clean up Wash or agent adapters.
- Run real-provider smoke sessions in temporary projects attached to an isolated
  test instance. Do not enroll, resume, stop, or alter the user's current agent
  session for verification. Preserve user provider/MCP configuration.
- A later live deployment is a separate user action. Report built/tested artifacts
  and results without installing them into the desktop hosting this conversation.

### Quality gates

Follow [TESTING.md](TESTING.md), checking the actual Makefile and fixture before
execution: some documentation still describes older entrypoints/layouts. Tests
must establish observable behavior and failure handling, not merely mirror the
implementation. Every milestone needs its evidence before being called complete.

| Area | Required checks |
| --- | --- |
| MCP and identity | Both adapters discover/call tools; user MCP configuration survives injection; spoofed sender/cross-workspace IDs and revoked credentials fail; failed launch leaves no child or half-created membership |
| Inbox/dispatcher | Reply before yield; delivery during a turn; concurrent human and agent input; ordered delivery; retries without duplicate acceptance; no overlapping ACP prompts; Stop retains mail without immediately waking the member |
| Completion/lifetime | Turn end never implies task completion; result persists before ephemeral retirement; resident receives multiple assignments; caller can teardown without deadlocking its own tool call |
| Failure/recovery | Adapter exit during work; service restart during dispatch; persistence failure reports failure rather than successful send; uncertain delivery is visible; pending decisions and messages remain attributable |
| GUI | Ordinary window before setup and after teardown; failed setup leaves it intact; reconnect restores correct workspace; transcript preview preserves controller ownership; scroll/selection remain stable during updates |
| Progress/documents | Single-item patch leaves other fields/items intact; stale edits and duplicate IDs reject; reload retains list/order; external edits and atomic document replacement refresh; missing documents recover visibly |
| Attention | Agent emoji/status update live without overriding runtime failure; flash appears over another app and with Agent closed; click-through identifies the correct member; human decisions remain distinct from flash messages |
| Existing behavior | Ordinary prompts, approvals, attachment handling, Agent/Agents window ownership, history/resume, and editor transcript consumers continue to work |

Verification sequence, inside the isolated build/test tree:

1. Build prerequisites and run focused backend/FE tests while implementing each
   slice. Exercise dispatcher and persistence concurrency under Go's race detector.
2. Run deterministic full-stack fake-adapter tests for the feature and the existing
   Agent lifecycle regressions. Wait for explicit events/assertions instead of
   timing sleeps; teardown must leave no harness-owned processes or watchers.
3. At integration, run `make unit-test`, `make test-race`, `make e2e-test`, and
   `make standalone-smoke`. The current `e2e-test` exercises the shipped multicall
   layout; the standalone smoke covers its distinct launch/packaging path.
4. Run bounded real Codex and Claude adapter checks for MCP discovery, one tool
   mutation, resident yield/wake, and session load with the bridge retained. Record
   adapter versions and actual capability limits. A fake passing does not prove
   vendor support; a missing real-provider check is an explicit validation gap.
5. Add VM/remote/layout or packaging gates when implementation touches those paths,
   as required by the repository. Document any skipped prerequisite and its impact.

Record commands, results, and unresolved failures with the implementation. Inspect
the final diff against the contract and regression matrix. Do not dismiss failures
as flakes, claim unrun checks passed, or broaden/repeat passing suites without a
new change or unresolved concern to justify it.

## 11. Implementation defaults and compatibility checks

The first implementation uses these defaults:

- Child context: fresh sessions with explicit role instructions, assignments, and
  file references. Resident sessions retain their own conversations across turns.
  Do not copy the orchestrator's whole conversation into every child.
- Child provider/model: a catalog slot or model id, with explicit effort and
  settings at spawn. Without a model, the catalog's default slot; report an
  unsupported explicit selection rather than silently substituting. Verify how
  role instructions interact with the existing global default prompt.
- Human intervention: allow the user to inspect and message any member, using
  the same serialized dispatcher and clear human attribution. The orchestrator
  remains the main conversation. The detail layout can be judged in the first UI
  prototype without changing controller ownership.
- Concurrency: start with a configurable cap of four active child turns; idle
  residents do not consume execution slots. Queued work must be visible. Keep a
  separate finite member/session cap because idle adapters still use resources;
  the initial default is 16 members including the orchestrator. `max_active`
  accepts 1–16 and `max_members` 1–64. Manually typed prompts retain their normal
  Agent behavior; the cap governs automatic inbox dispatch.

Engineering checks, requiring evidence rather than user preference:

- Choose the smallest IPC route for the stdio MCP bridge and finalize schemas.
- Prove MCP injection and subsequent session loading with the installed Codex and
  Claude adapters. Include a resumed resident receiving the same tools and identity.
- Prove an agent can finish a turn after `member_update.waiting`, then answer a later inbox
  delivery with retained context. Include the reply-before-yield race.
- Prove setup/teardown remains callable from the same top-level conversation and
  verify all lifecycle operations return without waiting on that calling turn.

Worktree management and dependency interpretation remain agent responsibilities.
Automatic restart recovery, cross-host swarms, nested progress trees, and a
declarative workflow engine are outside the initial slice.


## 12. Implementation map and current limits

- `internal/swarm` owns atomic persisted state and message/assignment transitions.
  State is stored under `$XDG_STATE_HOME/wash/workspaces.json` (the normal user
  state directory when XDG_STATE_HOME is unset). Archived workspaces remain in
  this file; transcripts remain in Agent History. There is no archive browser in
  the new sidebar yet.
- `internal/workspacemcp` implements stdio discovery and tool calls. Agentd and
  the multicall binary both understand `--workspace-mcp`. Per-session credentials
  connect it to a private local Unix socket, and are revoked with the session.
- `apps/agentd/be/workspace*.go` injects the MCP server before ACP new/load,
  serializes inbox delivery with ordinary turns, publishes sequenced view deltas,
  writes the plan and QA files, and routes authenticated human actions.
- `apps/ai/fe/src/WorkspaceSidebar.tsx` renders the team, decisions and the
  links to the Plan and Questions tabs; `WorkspacePlanGraph.tsx` draws the plan
  (laid out by `plan-layout.ts`); `WorkspacePanels.tsx` the member preview and
  human message controls. Previewing retains the current controller and shows
  the latest 300 transcript events. Plan changes travel as upserts/removals of
  nodes; a missing sequence triggers resynchronization.
- `workspace_get` adds JSON configuration, launch snapshots and live adapter settings;
  `workspace_configure` persists atomic configuration patches. `workspace_get` reports
  members, plan, assignments, pending decisions and delivery
  counts. It avoids replaying the whole inbox, and cuts finished assignments'
  texts to their first line. `inbox_read` accepts `after` and `limit` (default
  50, maximum 100, and at most 64 KiB a page), returning messages, cursor and
  has_more. `about` lists the open workspaces and this agentd's binary.
- Tool arguments are bounded to 1 MiB; individual messages to 32 KiB, plan
  nodes to 500, and messages to 10,000 per workspace. This initial store rewrites its
  atomic JSON snapshot on mutations; it is intended for project coordination,
  not high-throughput event ingestion.
- The runtime rechecks idle members without spending model turns. After a service
  restart, it restores paused state and marks interrupted deliveries uncertain.
  Resume is deliberate. Worktree allocation, test gates, merge policy, and
  role-based read-only behavior remain agent instructions; dependencies are the
  plan's needs, which gate the start of work.
- A real Codex adapter called the injected tool both before and after session
  reload. Claude session creation succeeded, but its prompt failed with expired
  OAuth credentials. A real Claude tool round trip remains unverified.


## Sidebar activity and context counts

Each team row shows provider-reported context tokens used and, when available,
the context-window capacity. These are current context counts, not cumulative
input/output or billed totals. Missing telemetry says “Context tokens not
reported.” Live usage arrives with the existing workspace frame; turn and
retirement boundaries checkpoint the latest counts so completed ephemeral members
retain them. A new workspace in the same orchestrator conversation cannot rewrite
the previous workspace's archived counts. Telemetry does not invalidate the
workspace coordination revision used by `workspace_configure`.

Activity comes from live ACP events and the turn gate, rather than replaying the
transcript or asking the model for status. The row shows a colored dot and a text
label: violet thinking, cyan tool use, green responding, blue working, pale blue
awaiting a message, amber human input, and red failure. Tool use also shows the
current tool title. Concurrent tools remain active until all finish. A late event
after a turn ends cannot restore a busy indicator. A member's durable waiting
reason takes effect only after its turn yields. Lifecycle states override activity.
Active states pulse; idle/waiting/ended states are still, and reduced-motion
preferences disable pulsing. Color is accompanied by text in every state.

Human transcript entries retain literal text and now use stronger weight, a themed
blue foreground/background and an accent border. The composer also uses a stronger
weight and foreground. Shared Agent views use the same treatment. Workspace JSON
readback includes live activity/detail/usage alongside saved member usage.
