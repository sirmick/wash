# Wash issues observed in the Redoubt workspace

Owner request: keep a running report of Wash failures met while orchestrating Redoubt. Record
evidence and workarounds; do not count application failures or expected denials as Wash bugs
without evidence. No credentials.

Workspace: Redoubt (M1), orchestrated from the owner's Claude Code session.
Project: `/home/mcloonan/redoubt`
Observed server: Wash `0.17.1`, workspace API `4.0.0`, agentd started 2026-10-01T06:27Z.
Adapter: `@agentclientprotocol/claude-agent-acp` `0.85.0`.

| Instance | Workspace ID | Notes |
|---|---|---|
| 1 | `13c482b7a10be3f3177768eed74b91ee` | Opened 2026-10-01 from `.wash/workspace.toml`, resuming `.wash/plan.toml` rev 77 and the QA threads. |

## WASH-R01 — `capability:"reviewer"` is pinned to two adapter versions, with no path forward

Date: 2026-10-01
Status: addressed: unverified enforcement is advisory and recorded on the Reviewed-by trailer (docs/AGENT_SWARM_BULK.md, R01); reconciled 2026-10-11. The reviewer allowlist on newer adapter versions is WASH-R22/R30.
Category: capability enforcement / adapter version pinning / launch UX.
Impact: medium-high; the whole review panel of the project's process (6 of 9 members) failed to
launch on first attempt.

### Reproduction

1. A host with `@agentclientprotocol/claude-agent-acp` 0.85.0 (whatever `npx` resolves today).
2. `workspace_configure` with six members `role:"reviewer", capability:"reviewer"` on provider
   `claude`, as `.wash/PROJECT.md` prescribes for the light tier and the red team.
3. Every one fails:

```text
reviewer capability unsupported by @agentclientprotocol/claude-agent-acp 0.85.0:
requires verified claude-agent-acp 0.81.1 or 0.81.2; mode names are not read-only guarantees
```

Members are committed in `failed` state. Implementers and the Architect on the same provider
launched fine.

### Why it is there

`apps/agentd/be/workspace_capability.go`: `reviewerVerifiedVersions = {"0.81.1","0.81.2"}`,
with the comment that unknown adapter versions must be reviewed before Wash claims their launch
metadata (tools allowlist, `disallowedTools` merge, `settingSources`, `strictMcpConfig`,
`allowBypass`) still works. The pin is deliberate and the reason is sound.

### What is wrong

- **The pin has no release process.** The adapter moved four minor versions since the
  verification; nothing in Wash re-verifies, warns at startup, or tells the owner which version
  to install. A fresh machine gets the newest adapter and the feature is simply gone.
- **No owner override.** The failure is terminal: there is no `capability:"reviewer-unverified"`
  or a workspace-level `permissions.allow_unverified_reviewer` recorded on the member, the way
  `override` records an early start on a plan node. The only way out was to drop the capability
  entirely, which is the worst of the three states (unenforced *and* unrecorded).
- **Not discoverable before launch.** `about.permissions.reviewer_capability_profiles` lists the
  verified versions but not the installed one; `preview:true` does not run this check. The
  orchestrator learns at launch, after the members are committed.
- **Partial commit.** Six failed members now sit in the workspace; re-sending each key with
  `capability:null` relaunched them, but the failed definitions had to be rewritten in full.

### Desired behaviour

1. At agentd start (or on `view:"about"`), report the installed adapter version next to the
   verified list, and say plainly whether `capability:"reviewer"` is available on this host.
2. Validate in `preview:true` and refuse there, before anything is committed.
3. Offer an owner-authorised degraded mode that is *recorded*: launch with the same tool
   allowlist on an unverified version, mark the member `enforcement:"unverified"` in its
   launch metadata and in every result it produces, so the merge trailers show which reviews
   ran without enforcement.
4. Make the verification itself a script against the adapter's `dist/acp-agent.js` (the
   comment lists the exact points), so bumping the pin is a `make verify-acp` away rather than
   a code review.

### Workaround applied

Owner decision 2026-10-01: relaunch the six reviewers without `capability`; read-only rests on the
role instructions, and the orchestrator checks each worktree's diff against the implementer's
commits before a merge.

## WASH-R03 — A handoff written with `member_update {handoff}` reaches nobody

Date: 2026-10-01
Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

Wash's `context_warn` lifecycle note tells the orchestrator to have the member write a handoff,
end it and relaunch with `handoff_from`. The Architect wrote its handoff at 08:31 (stored at
`.wash/local/handoffs/architect.md`) and set waiting. The orchestrator received nothing: no
"handoff written" lifecycle note, and no message, because a resident with no assignment has no
result to complete. The orchestrator learned of it two hours later by asking. Desired: a lifecycle
note "<member> wrote its handoff" (as there is one for `context_warn`), or `member_update {handoff}`
delivering a progress message to the orchestrator; and the stored path named in the note.

## WASH-R02 — Model slot `opus[1m]` is not in this host's catalog; the Architect failed silently in `from:` load

Date: 2026-10-01
Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

`workspace_configure {"from": ".wash/workspace.toml"}` committed the workspace, then the
Architect (`model = "opus[1m]"`, the ID PROJECT.md prescribes) failed with
`unsupported value "opus[1m]" for model; available values: default, opus, ...`. Two notes:

- The catalog reported was `anthropic-pro`, where PROJECT.md expects `anthropic-budget`; the
  `[1m]` suffix exists in one and not the other. Which catalog a host serves, and that a
  workspace file's model IDs are catalog-specific, is not visible until a launch fails.
- Correcting the member needed `name` and `instructions` re-sent as well (two more round trips:
  "name is required", then "instructions are required"), although the file had supplied both.
  A patch to a failed launch should keep the committed definition and take only the changed
  fields, as the receipt's "omitted fields stay" promises.

---

# Orchestrator feedback after one day (2026-10-01), with examples

Everything below was observed in workspace `13c482b7a10be3f3177768eed74b91ee`. An agent working
on Wash can read the evidence in `/home/mcloonan/redoubt/.wash/` (`plan.toml`, `qa/*.md`) and
`/home/mcloonan/redoubt/.wash/local/` (briefs, handoffs, progress files; untracked). Threads named
here are files `qa/<id>.md`; their event bodies quote the exact tool traffic.

## WASH-R04 — A thread's answer reaches its creator and assignee, not the member it is about

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

`message_send` with `qa:{action:open}` makes the sender the creator and the recipient the
assignee; "an answer on a thread also reaches its creator and assignee". When the orchestrator
opens a thread *on an implementer's behalf* (the normal case: the implementer asks the
orchestrator, the orchestrator frames it for the Architect), the Architect's answer comes back to
the orchestrator only, and the implementer learns nothing until the orchestrator forwards it.

Examples (each `qa/<id>.md` shows the Architect's answer followed by an orchestrator message
restating it to the implementer):
- `GATE1-notice-late`: four Architect answers, four forwards to `gate1-implementer`.
- `INIT1-root-pages`: three items ruled; forwarded to `init1-implementer-2` on 2026-10-01 at
  the events after `a20aec4c…` and `fe4fdde8…`.
- `INIT1-own-budget`: ruled `d4628203…`; forwarded to `init1-implementer-3`.
- Contrast `IPC2-steward-family`, which the implementer opened itself: the owner's decision and the
  Architect's answer reached it directly, no forward needed.

Desired: `qa.open` takes `on_behalf_of:<member>` (or `participants`), and answers reach every
member on the thread's node that has sent a message on it.

## WASH-R05 — One active assignment per member blocks "next step" and turns reports into dead ends

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

`assignment_update {create}` on a member with an active assignment is refused
("member already has an active assignment"), and a `progress` message does not close one. So when
an implementer reports a checkpoint as *progress* (not a result), the orchestrator cannot queue the
next step; it must send an `instruction` with `assignment_id`, which works but is not on the plan.

Examples:
- `init1-implementer` reported D1 as progress (`62ffb07d…`, 2026-10-01); the follow-up
  "WIP-commit and hand off" could not be an assignment (refused `1e08b600…`-era call) and went
  as an instruction (`99759832…`).
- `ipc2-implementer`: the fix round was an assignment, the mid-round handoff instruction had to
  ride on it (`b1882118…`).

Desired: allow one queued assignment per member, or make an instruction on an active assignment
a first-class, plan-visible step.

## WASH-R06 — Launching on an overridden node takes two calls and a round trip

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

`workspace_configure` with a `task` on a node whose needs are not done is refused:
"launch it without a task and assign with override". The override is accepted on
`assignment_update` but not on the launch. Every INIT1 launch (three implementers) was therefore
launch → wait → `assignment_update {override}`; the override text was retyped three times and the
node now shows `3 override(s)` for one decision. Desired: `task_override` (or `override`) on the
member definition, recorded once on the node.

Related: a corrected definition for a *failed* launch had to re-send `name` and `instructions`
("name is required", then "instructions are required": `architect`, 2026-10-01 ~06:40), although
the committed definition had both and the receipt says "omitted fields stay".

Related: `max_members`/`max_active` are top-level `workspace_configure` parameters, but nothing in
`about` says so; nesting them under `workspace` gives `json: unknown field "max_members"`.

## WASH-R07 — `expected_revision` is required but only discoverable by reading the file

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

Resolving a thread or editing a started node needs `expected_revision`, which no read tool
returns: the orchestrator `grep`s `Revision:` in `qa/<id>.md`, and the first attempt still
conflicts whenever a member posted in between (`INIT1-root-pages` rev 4→5, `IPC2-steward-family`
6→7→8 in one minute, `K13` 3→4). Desired: `plan_get` and a thread read return revisions, or the
orchestrator may resolve/edit without one (it already holds the authority).

## WASH-R08 — Paths in messages are not resolved against the project root

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

Members run in different `cwd`s (worktrees under `.worktrees/<pkg>`). A relative path in a message
(`.wash/local/K14-progress.md`) resolves for the implementer in the project root and fails for
its reviewers in the worktree; two K14 reviewers reported "does not exist in my worktree"
(`k14-simplifier` `1a953d61…`, `k14-editor` `45922bc1…`). Wash knows every member's `cwd` and
`project_root`. Desired: resolve `.wash/…` paths against `project_root` when delivering, or
document "always absolute" in the role guide.

## WASH-R09 — The 2000-byte ceiling is right for threads and wrong for handoffs and briefs

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

Members route around it correctly (detail in a file, pointer in the message), but a handoff that
is mostly a pointer costs the successor a cold read of a long file, and the first thing a
successor needs (branch state, traps) is the part most likely truncated. See
`.wash/local/handoffs/architect.md` versus the 2000-byte handoff Wash delivered to `architect-2`
(the launch receipt's `handoff` field, 2026-10-01 ~09:30). Desired: handoffs and briefs up to
~16 KB; results and thread bodies stay at 2000.

## WASH-R10 — No context-usage visibility before the 30% note

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

The only signal is the `context_warn` lifecycle note. With it, handoffs landed wherever the member
happened to be: `ipc2-implementer` mid-fix-round (`d1c080a0…`), `init1-implementer-2` mid-D3
with a design question in flight. Showing each member's context share in `plan_get`/state would
let the orchestrator time a handoff at a commit boundary. Also: the note fires once; a member that
keeps working to 45% is not re-warned.

## WASH-R11 — The orchestrator's own context is spent on routine events

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

With ~20 members over ten hours, every `progress`, `lifecycle` and `note` event is a turn in the
owner's own session (the orchestrator is that session). The work was fine, but the orchestrator's
own 30% point will arrive from bookkeeping rather than decisions. Desired: a digest mode where
progress and lifecycle notes are batched or summarised, and only results, questions and owner
decisions wake the orchestrator.

Addressed in API 4.2: `digest` on the workspace (e.g. `"5m"`) holds progress, notes and
lifecycle for one turn, or until a result, question, answer or owner decision carries them.

## What worked (keep)

- The plan and QA files resumed intact on a new host from git alone (`workspace_configure
  {from: ".wash/workspace.toml"}` → plan rev 77, every thread).
- `needs` gating with recorded overrides (INIT1 refused twice until a reason was given).
- Threads with `decision_refs` and resolve-with-evidence: nine rulings today, each a thread → a
  commit → a page (`GATE1-notice-late`, `INIT1-root-pages`, `INIT1-own-budget`,
  `IPC2-steward-family`).
- `assignment_update {wait}`: three-reviewer rounds arrive in one turn (K14, K15, IPC2).
- `handoff_from`: four handoffs, each successor productive within minutes
  (`init1-implementer` → `-2` → `-3`, `ipc2-implementer` → `-2`, `architect` → `architect-2`).
- The supervisor never nagged.

---

# Redoubt orchestration follow-up (2026-10-04)

Workspace `8a005a3cee5b786ad913a8412bb1b22f`, orchestrator
`5e72d6c884926b33a54b579af9bb50de`; Wash 0.17.1 / workspace API 4.0.0.
This run used the Codex adapter, the `openai-pro` catalog, three implementation lanes
(SCHED1, MEM1, BEAM7), a resident Architect, and three reviewers per package.
Evidence below is from tool responses and delivered messages, not a daemon trace investigation.
No Wash code was changed for this report.

## WASH-R12 — Queued control messages lag behind the work they govern

Status: delivery made legible in API 4.2 (dispatched_at and watermarks, docs/AGENT_SWARM_BULK.md, R12); the idle-member-never-wakes cause itself is still open, with WASH-R25 and NVL WASH-005. Reconciled 2026-10-11.

Members repeatedly reported waiting for permissions already sent, or continued older work
after a context-checkpoint instruction was queued. Examples:

- MEM1 received grants for its initial QEMU/docs windows in
  `d5470b3dcc7ce3051cbd6b3be5c621ce`, and scoped size-budget edits in
  `32be182c07abd2e36f6fd127ccdc2f72`. Later question
  `0fd8ae71f082ed977d8b154a5e450f4f` requested those permissions again.
- BEAM7's successor received its disjoint `case.rs` HostTests window in
  `044bc1056e78a353ba21aded07a0646a`. Later progress
  `2b9f741b30248089866dbe7abecb533f` still said it was waiting for that handover.
- MEM1's 70% context warning was `48c9c2b32531293914b817e61843feda`.
  Checkpoint instruction `f2e1a39f75d8f5ada371542868d9d320` was queued; later live
  team state showed 224,840 / 258,400 tokens and ten queued messages, with no saved
  handoff yet. We interrupted and issued checkpoint-only instruction
  `e4642b7608ba072250f9bced4be136a0`.

This does not prove message loss. Some tools were long-running (MEM1's model host test alone
took 382.6 seconds), and old reports can legitimately arrive after newer instructions.
The problem is that an orchestrator cannot reliably tell whether a permission or stop request
has reached the member's active reasoning, rather than merely its transport queue.

Desired: expose queued, dispatched, and consumed/acknowledged sequence watermarks, with
timestamps; give checkpoint/stop instructions a supported priority boundary; let an instruction
explicitly supersede older instructions. A safe checkpoint operation should stop new work,
preserve any in-flight tool/session, and emit a handoff-ready event. Avoid requiring repeated
free-text reminders or blind session termination.

Addressed in API 4.2 (2026-10-04): messages carry `dispatched_at`/`settled_at` and a
`superseded` state; `workspace_get team` shows per-member `mail` watermarks (queued, in_turn,
oldest_queued_s, last_dispatched_s_ago, last_settled_s_ago); `message_send` takes
`priority:"checkpoint"` (leads the next turn as its one ask, supersedes the sender's earlier
queued instructions); `member_control checkpoint` is interrupt plus that instruction.

## WASH-R13 — Stall alerts can conflict with current member activity

Status: addressed with R12 in API 4.2 (docs/AGENT_SWARM_BULK.md); reconciled 2026-10-11.

- Lifecycle `3a4eed855fbf0fa24680d0b1a1e2b783` said SCHED1 had three messages queued
  for 9m37s, was in no turn, and should be checked for relaunch. The subsequent team read
  showed it responding, with new pinned-test results already delivered.
- Lifecycle `d58ae442b92c06b5c1e07c6eb04085da` said BEAM7 had three messages queued
  for 13 minutes and was in no turn. The subsequent read showed it thinking with an active
  assignment and preserved source changes. We did not infer a dead session from that alert.

Desired: include observation time, member/session generation, last activity and delivery
watermarks in the alert; revalidate before recommending end/relaunch. Distinguish a stale
status string, a long tool call, a queue awaiting a turn boundary, and a dead adapter. Recovery
should first preserve work and reconcile deliveries. This run did not establish a daemon crash.

Addressed in API 4.2: `wedged` and `undelivered` fire only on two consecutive supervisor
checks, say since when they held and when the member's last delivery settled, and the
lifecycle message carries its observation time. Still open: the alert is free text, not
structured fields; and the possible `agentRunning`-stuck blind spot is unconfirmed.

## WASH-R14 — Late predecessor confirmations create avoidable orchestration turns

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

After SCHED1 had been checkpointed and its successor launched, predecessor message
`fa17c2e6418c533377288dc179b4523e` arrived confirming the same checkpoint and saying
it was waiting for replacement. BEAM7 likewise sent confirmation
`a945e8b736e99bff3a8279718d8332b8` after its successor was already launched. These were
valid historical messages, but needed no new action. Duplicate Architect ruling deliveries
`e8e9944a2a5a6e6c9c747599d2301962` and `f2824278c6e7d31036b9dab3808c3f0d` also arrived
together (likely direct plus thread fanout; not established as a transport defect).

Desired: emit one structured handoff-ready event with path, revision and successor relation;
annotate mail from ended/replaced members as historical. Deduplicate equivalent thread/direct
notifications for the same recipient, or expose their shared provenance. Digest routine
progress/confirmations without waking the owner's orchestrator for each one. Keep genuine
questions, failures and results promptly visible.

Addressed in API 4.2: mail from an ended member is labelled `(…, ended)` in the
orchestrator's inbox; the same words from the same author on the same thread are not sent
twice when a QA reply and a direct answer both carry them; routine mail digests (R11). Open:
a structured handoff-ready event beyond the R03 lifecycle note.

## WASH-R15 — Message, QA event and thread identifiers need clearer affordances

Status: addressed (recorded in docs/AGENT_SWARM_BULK.md, which cites this number); reconciled 2026-10-11.

The human IPC decision had message ID `9e35cd7b11fd13a5f1e4c0d5db0d75b1` but QA event ID
`9d1354d63f10b0774a745ede130f3de6`. We successfully reconciled both with a targeted QA read.
Separately, BEAM7 tried to retrieve reviewer result message
`5c69fb098b4e32d0b261e92380ba7a74` as a thread and got `unknown QA thread`; its successor
needed the findings copied inline. A `reply` cannot attach decision_refs, while a guarded
thread update also requires creator/assignee authority; the Architect needed the orchestrator
to attach the actual decision event.

Desired: typed references or directly usable lookup arguments in result metadata, a small
message-by-ID read, and an error identifying a known message/event supplied as the wrong kind.
Document decision_refs' expected identifier beside each returned decision. Do not force a
full workspace-state/history read to retrieve one review result.

## WASH-R16 — The owner's questions and the orchestrator's answers drown in the inbox stream

Status: owner-reported 2026-10-04; addressed in the Agent transcript (web/lib/src/agent-session.tsx).

The orchestrator is the owner's own Claude Code session. Its transcript interleaved the
owner's prompts with every inbox turn (members' progress, results, the supervisor's notes),
and the orchestrator's reply to the owner was ordinary prose, indistinguishable from its
prose about members' mail. The owner's question had a blue rule; the answer had nothing.
The data model already kept the two apart — an owner prompt is a `user` event, an inbox
batch a `collaboration` event, and every event after one belongs to it until the next —
only the rendering merged them.

Change: a transcript with inbox turns grows an `Owner | Team` tab strip above it, the composer
below both. Owner shows the owner's prompts and the orchestrator's prose/images in reply, with
a badge counting prompts not yet answered; Team is the whole transcript, with a badge for inbox
turns that arrived while the Owner tab was open. In either lane the reply to an owner prompt
carries a thinner version of the prompt's rule. Inbox batches now render one block per message:
`progress`, `lifecycle`, `note` and `flash` collapse to their label and first line until
opened; `question`, `result`, `answer` and `decision_response` stay open with a rule.
R11 (digesting routine events on the orchestrator side) remains separate.

## Model catalog follow-up (2026-10-04): "opus-5-5 is sometimes available"

Not a Wash catalog bug as such, but Wash made it look like one. Wash has no model list of its
own: `resolveCatalog` passes a raw model id through, and the only check is at launch against
the option list the live `claude-agent-acp` session reports — which comes from the Claude Code
CLI's `initializationResult.models`, filtered by Claude Code's own per-project
`availableModels`/`deniedModels` settings, and which drifts with CLI/adapter updates. On this
host the list has contained `opus` and `claude-opus-5` and never `claude-opus-5-5`; the
owner's `~/.config/wash/agents.json` had overridden two catalogs to `claude-opus-5-5` (and one
slot to `sonnect`), so launches failed whenever a slot resolved to them. Fixed the config.
Desired in Wash: validate slot models at save time and in preview against the remembered
adapter list (warn, not refuse: the list is per-host); seed the CatalogPane model field from
that list; have the launch error name the catalog and slot, not just "model".

## Owner ruling (2026-10-04): provider-agnostic, advisory launch settings — API 4.2

The owner's rule after reading the above: Wash must be agnostic to the AI provider, so
`capability:"reviewer"` and a member's model string are advisory, not mandated. API 4.2
implements it. A reviewer launches on every provider; Wash's host guards apply everywhere and
the adapter's allowlist where it has one; the member's `applied.enforcement` records
`adapter`, `unverified` or `host`, the `Reviewed-by` trailer carries the level when less than
`adapter`, and the configure receipt's `advisories` (preview too) say what will not hold before
commit. `subagents:"deny"` records `denied` or `instructed`. A model, effort or config value
the adapter does not offer runs on its default with `applied.notes` saying so. The 4.1 gate
(`enforcement:"unverified"` as the way through) is replaced by `enforcement:"adapter"` as the
one way to be refused. This closes R01's "no path forward" and the model-catalog failure mode
above. The catalog-editor validation followed the same day: a slot model not in the list the
adapter last reported is flagged in the Setup tab, noted on the slot in the launcher, and an
advisory in the configure receipt.

Addressed in API 4.2 (R15): `workspace_get view=message id=…` reads one message (sender,
recipient or orchestrator) or one QA event; a `thread_id` that is really a message or event id
is refused saying what it is and where its thread is; `decision_refs` are documented as free
text. Open: typed references in result metadata.

## Model portability and reviewer setup (R01/R02 follow-up)

The checked-in Wash catalog still maps `anthropic-budget.frontier` to `opus[1m]`, rejected
by this host. Slot-based project configuration works well, but preview validates catalog/flags
without establishing that the adapter will accept the resolved model at actual launch.
Report that distinction clearly and consider a catalog health check. We moved Redoubt's
provider/model choices out of project policy and used `frontier`, `coding`, `small` slots.

The Codex adapter honestly reports that enforced `capability:"reviewer"` is unsupported.
That restriction is not itself a Wash bug. Our project initially made enforcement mandatory,
creating an unnecessary owner question; the owner explicitly chose OpenAI reviewers instructed
not to modify source. We removed the project requirement and launched those reviewers, without
claiming tool enforcement. Explicit launch metadata such as `review_policy: instruction_only`
versus `adapter_enforced` would make that intentional choice easier to audit across catalogs.

## Improvements verified since the earlier report, and orchestration lessons

- R07's missing revision visibility was not reproduced: targeted `plan_get` and QA reads
  returned revisions, and guarded updates worked.
- R10's missing usage visibility was not reproduced: team state reports used/capacity, and
  the 70% lifecycle warnings fired. The remaining problem is timely checkpoint delivery.
- `handoff_from` successfully launched Architect and implementer successors with preserved
  worktree commits. Catalog slots resolved correctly for the OpenAI team. Read-only reviewer
  instructions were followed in the reviews received so far.
- Some delay was our own orchestration: too many relay messages, broad file-window locks,
  and handoffs requested while old permission messages were still queued. We narrowed
  `case.rs` ownership to disjoint HostTests and Boot sections in separate worktrees, allowing
  independent progress. Do not count that initial over-serialization as a Wash defect.
- Prefer concise current-state messages over accumulated reminders. A structured pending
  controls/permissions view and handoff-ready event would reduce both agent error and noise.

No credentials or private source payloads are included. Message IDs refer to this workspace;
briefs, logs and saved handoffs are under `/home/mcloonan/redoubt/.wash/local/`.

---

# Redoubt resume (2026-10-05 evening)

Workspace `7a63bc6e1f6ea77266eccffc586b0825`, orchestrator `cab495a4d2c090f3d5882b1a537494b9`
(the owner's Claude Code session, Fable 5.1); Wash 0.17.2 / workspace API 4.2.0; adapter
`@agentclientprotocol/claude-agent-acp` 0.86.0; catalog `anthropic-pro`. Cold start from a
clean checkout after the 2026-10-05 graceful stop: no workspace open, plan and 160+ QA threads
resumed from `.wash/` alone. Evidence is tool responses in this session; no daemon trace read.

## What worked (keep)

- `workspace_configure {from, workspace:{name, project_root}}` with `preview:true` then for
  real: one call restored plan rev 2 (89 nodes) and every thread, and launched the Architect
  from `handoff_from:"architect-14"` with `subagents:"deny"` applied (`applied.subagents:
  "denied"`), `auto_approve:true`. Two implementers launched the same way in one patch, each
  with its worktree `cwd`, and the receipt showed the resolved model, launch settings and
  `applied` for each. No failed launches, no round trips.
- The API 4.2 answers to R07, R09, R12 and R15 held: `plan_get node detail` returned the node's
  revision; the launch receipt carried the Architect's whole stored handoff; `about` documented
  the mail watermarks and `priority:"checkpoint"`; `view=message` exists.
- `member_update {waiting}` returned at once with the "finish your turn" instruction.

## WASH-R17 — A QA thread file is 60% base64 checkpoint

Status: addressed 2026-10-11: the checkpoint is plain JSON on the file's second line, so `tail` reads only events; `view=qa thread_id` takes `last:N`. Keeping the checkpoint out of the readable file altogether is still open (AGENT_SWARM_BACKLOG.md).

`.wash/qa/IPC3-wake-latency.md` is 195,102 bytes; its trailing
`<!-- wash-qa-checkpoint-v2: … -->` blob is 115,876 of them (one line). Every thread file ends
in one. The project rule is "read one thread, never the directory", and the natural read is
`tail`: a member that tails the last three events gets the blob too, and one `tail -120` cost
this orchestrator ~128 KB of tool output (persisted to a file by the harness, otherwise it would
have been in context). The blob re-encodes the events already in the Markdown above it.

Desired: keep the checkpoint out of the readable file (a sibling `.wash/qa/.state/<id>.json`, or
the plan file's mechanism), or at least put it first so `tail` is safe; and give `workspace_get
view=qa thread_id` a `last:N` (or negative `after`) so the newest events can be read without
knowing an event id.

## WASH-R18 — `workspace_get` with no workspace returns `null`

Status: addressed 2026-10-11 (e7a30827): the answer is `{workspace: null, hint}` naming `workspace_configure`.

Before setup, `workspace_get {view:"team"}` returned `null` (no error, no hint). PROJECT.md's
startup step 1 is "`about`, then `workspace_get({})`": the orchestrator has to infer "no
workspace" from a bare null. Desired: `{workspace:null, hint:"no workspace; workspace_configure
{from} creates one"}` or an error naming the next call.

## WASH-R19 — `plan_get node=X` returns the whole plan as well

Status: addressed 2026-10-11 (e7a30827): `node` returns that node's subtree only.

`plan_get {node:"BEAM7", detail:true}` returned the node's detail *and* the full 89-line node
list, identical to a bare `plan_get`. Three node reads in one turn cost three copies of the plan
(~4 KB each). Desired: `node` returns that node (and its subtree) only; the full list on request.

## Catalog follow-up: `coding` still resolves to a model this adapter rejects

Status: open on this host (owner said "let's discuss" on 2026-10-04). The `anthropic-pro`
`coding` slot still maps to `claude-opus-5-5`; the adapter's list has `opus` and `claude-opus-5`
and not that id, so every implementer launch overrides `model:"opus"`, which puts a model id
back into the orchestrator's hands — exactly what SWARM.md's slot rule is meant to avoid. API
4.2 says an unoffered model "runs on the adapter's default, recorded in applied.notes"; the
2026-10-04 handoff reports it was *rejected* at launch instead. Not reproduced today (I did not
try the slot); worth one launch with `model:"coding"` to see which behaviour 0.17.2 has. Either
way the fix is the catalog entry: `coding -> opus`.

## Reviewer verification pin is at 0.81.x; installed is 0.86.0

`reviewer_capability_profiles.claude.enforcement:"unverified"`, `installed_version:"0.86.0"`,
`verified_versions:["0.81.1","0.81.2"]`. API 4.2 makes this advisory (reviewers launch, the
trailer says `[enforcement unverified]`), which is the right behaviour — noting only that the
pin has not moved in five adapter minors, so R01's "no release process" point stands.

## Orchestration notes (ours, not Wash's)

- Two members launched in one `workspace_configure`; the second (SCHED1) was told in its
  instructions to run no cargo/QEMU until an explicit grant, because the first holds the machine
  for a merge-gate whole bench. A workspace-level "machine token" (one holder, queue, release on
  result) would replace that free-text rule; today it rests on instructions and on the
  orchestrator remembering who holds it.
- `workspace.toml`'s Architect entry carries a comment "add subagents=deny at launch when the
  adapter supports it"; since `from` merges with call fields, the orchestrator re-sends the
  whole member definition (name, instructions, lifetime are required) to add one field. A
  `members.<key>` patch on top of `from` that needs only the changed field would make this a
  one-liner.

## WASH-R20 — Ephemeral members stay listed after their task completes, and count toward `max_members`

Date: 2026-10-06. Status: addressed 2026-10-11: a retiring member whose session is gone is ended by the dispatch loop instead of staying listed. The `preview:true` needs-check half is still open.

Five ephemeral editors (`bench-rule-editor`, `-2`, `-3`, `-4`, `fpga-editor`), each launched
with a `task` and each having completed its assignment with a result, still appeared in the
member list and counted toward `max_members` (16). A `workspace_configure` launching three
reviewers failed with "workspace member limit (16) reached"; I had to `member_control end` the
five by hand and raise `max_members` to 20. The `about` text says an ephemeral member "retires
after the turn" its task completes in; either it does not, or retirement does not free the slot.
Desired: an ephemeral member whose task is complete is ended by Wash (and shown as ended), and
the limit counts live members only.

Also seen in the same call: the whole configure was refused for one member's `needs` error
("node MEM2 needs MEM1 (active) first") after an earlier member in the same call had been
accepted in a previous attempt; the refusal is correct, but it would help if `preview:true`
reported the needs check too (it reports only the catalog and launch flags).

## WASH-R21 — A multi-update `assignment_update` fails whole on one update's `needs` check, and the error names only that update

Date: 2026-10-06. Status: addressed 2026-10-11 (e7a30827): the error says no update in the call was applied and to resend all of them. An override recorded on an active node now covers its later assignments too.

`assignment_update {updates:[a, b, c]}` where only `c` is on a node whose needs are not done
is refused as a whole ("update 2: ... node SMP1 needs IPC3 (reported) ... start anyway with
override"). Atomicity is fine; but the error does not say that `a` and `b` were NOT created,
and the natural reading ("update 2 needs an override") invites re-sending only `c`. I re-sent
all three each time after noticing, which is the safe move, but twice I had to re-check that
`a` and `b` were really absent. Desired: the error states "no update applied" explicitly, or the
call applies the updates that pass and reports the one that did not (with its index), since
creates are independent. Related: the `needs` check fires on every create for an active node
whose needs are unmet, even after the node already carries a recorded override from its launch;
an override recorded on the node could cover its later assignments too.

### WASH-R22 (2026-10-06 ~06:30): `capability: "reviewer"` members never run

Status: open (with WASH-R30). The launch records an advisory for an unverified adapter version; it does not yet say the member cannot report.

Five reviewers launched tonight with `capability: "reviewer"` (the launch refuses `approval: "auto"`
with it: "reviewer capability cannot be auto-approved") received their instructions
(`last_dispatched` updated) but then sat `idle` for 1-2 hours each with a few tens of thousands
of tokens used and never sent a message; `pending_approvals` stayed 0 the whole time, so nothing
surfaced for the orchestrator to approve either. The same members relaunched WITHOUT
`capability` and WITH `approval: "auto"` (read-only enforced by their instructions) answered
within minutes. Either the reviewer allowlist on claude-agent-acp 0.86.0 (the receipt says
"not among the versions Wash verified it on") blocks their first tool call silently, or the
approval prompt goes nowhere. Expected: a refused/blocked tool call should surface as a pending
approval or an error the orchestrator can see, and `workspace_get team` should say the member is
blocked, not `idle`.

### WASH-R23 (2026-10-06 ~11:30): plan_accept's Reviewed-by trailers carry stale verdicts

Status: open. `plan_accept` still takes each reviewer's recorded result (internal/swarm/plan.go, verdicts).

`plan_accept` builds `Reviewed-by:` trailers from reviewers' recorded assignment *results*, not
from their latest verdicts. Reviewers here renew verdicts by message (answers), so the trailer for
SCHED1 read `Red team: Merge verdict: BLOCK` (its round-4 result) when the red's final verdict was
OK (round 6, by message), and MEM2/VOL1's said "OK with notes" when the renewals were OK. I edit
the line to the true verdict before committing, which the instruction ("exactly as given")
forbids. Expected: a way to record a renewed verdict on the node (a reviewer's `member_update`
verdict field, or the trailer taking the newest verdict message on the node).

## WASH-R24 (2026-10-08): wash-fswatch reads every new file's content, not only its name

Status: open, attribution unproven (2026-10-11). `internal/fswatch` and `internal/remotewatch` open no files (inotify plus stat); the PID named is the multicall `out/wash`, which every Wash app shares, so the reader may be another app (a preview, a thumbnail, a file list). Reproduce with `fuser`/`strace -e openat` on the writing test before changing the watcher.

Measured on this host (ext4, relatime): a file written and given atime == mtime by utime has its
atime bumped to now within 50 ms, 40 of 40 times, with no reader of mine; a future atime, which
relatime never bumps, stays. The only watcher running is `wash-fswatch` (Wash's own, PID 95250,
/home/mcloonan/wash/out/wash). So the watcher reads (not stats) each new file. Effect: the beamlet
difftest's erlang/files2 test, which set atime == mtime and read it back, failed about 1 run in 3
on both the BEAM oracle and beamlet (B28), and a polluted cached oracle result then failed every
run. Expected: a watcher that needs only names and sizes should stat, not read; or read with
O_NOATIME.

## WASH-R25 (2026-10-08): a member launched with handoff_from sat idle for three hours with queued instructions

Status: open; the supervisor now reports `undelivered` after two checks, the wake-up itself is unexplained (with WASH-R12 and NVL WASH-005).

beamlet-red-2 (e07c9d7d) was launched from beam4-red's handoff. I sent it four review instructions over about three hours, and each send returned `delivery: queued`. The team view showed it `idle`, with no `waiting` field and no turn running. `member_control interrupt` answered "no turn running". It never took a turn until I sent another instruction after the interrupt. Nothing in the team view flags a member whose queued instructions go undelivered while it is idle. I only noticed because reviews stalled. Expected: an idle member with queued instructions starts a turn. Failing that, the team view shows "queued N, oldest Ns" for an idle member, and the supervisor nudges it.

## WASH-R26 (2026-10-08): handoff lifecycle messages repeat at every commit boundary, each saying "end it and launch its replacement"

Members told to keep a running handoff (member_update handoff at each commit boundary) produce a lifecycle message every time. The userland implementer sent eight in one afternoon. Each reads "When its open work is reported, end it and launch its replacement", though the member is at 29-50% context and working. That's noise that hides the real ones. Expected: the lifecycle text depends on context use (only suggest replacement above context_warn), or a handoff update can be marked as routine.

Status: addressed 2026-10-11: the first handoff is announced; later writes are silent unless the member is past `context_warn`.

## WASH-R27 (2026-10-08): decision_request blocks the orchestrator, so owner questions wait for idle moments

Status: open. Design: `decision_request` is blocking by decision (AGENT_SWARM_BACKLOG.md, 2026-09-26); a non-blocking form is unbuilt.

decision_request "blocks you: nothing else reaches you until then". With eight to ten members running, a blocking owner question would stall every review and merge until the owner answers. So I keep owner questions in a file (.wash/local/architect-questions.md) and raise them in prose, which the guide says not to do. Expected: a non-blocking decision_request whose answer arrives as a message, or a per-question timeout after which work resumes.

## WASH-R28 (2026-10-08): ending a member leaves its open assignment open, and the node shows "active with nobody on it"

Status: open.

When I end a member mid-package to relaunch from its handoff (SHELL3, SRV1, B37, BEAM17), its assignment stays open and the nudge says "Assign it again". The replacement gets a new assignment from its `task`, so the old one lingers. plan_accept refuses while any assignment is open on the node, so I have to watch for it. Expected: `handoff_from` transfers the predecessor's open assignment to the replacement, or `end` offers to close it as superseded.

## WASH-R29 (2026-10-08): no way to see a reviewer's queue

Status: open. The team view shows `undelivered` counts and the oldest age, not the queued instructions themselves.

Four reviews were queued on one red. I could not see that in the team view (`undelivered` counts only) or tell which instruction it was working on. Rebalancing reviews across reds needed my own bookkeeping. Expected: per member, the list of queued instruction ids and their first lines.

## WASH-R30 (2026-10-08): a capability:"reviewer" member with a launch task never completes it
steward3-red was launched with capability "reviewer" (advisory: "allowlist applied on claude-agent-acp 0.88.0, not among verified versions") and a task. It ran a turn (25k tokens), read its handoff, then went idle with the assignment open and no waiting set, three times, including after a checkpoint-priority instruction naming the exact member_update call. The supervisor nagged every 5 minutes. Likely the adapter's reviewer allowlist on the unverified version drops the Wash MCP tools (member_update, message_send), so the member can read but cannot report. Reviewers launched without capability:"reviewer" (prompt-only restrictions) work. Expected: either the allowlist keeps the Wash tools, or the launch receipt says the member cannot report (refuse with enforcement "adapter", or an advisory that names the missing tools).

## WASH-R33 (2026-10-10) — A decision_request blocks the Architect's every other thread

The Architect sent the owner a decision_request (handle generations, 23:59Z). From then on it shows `needs-input`, "Waiting for the owner", and two queued messages (an orchestrator question on an unrelated QA thread, SMP6-zeroing-billed-to-nobody, blocking an implementer; oldest queued 1,608 s). The owner was present but did not answer for an hour; the SMP6 ruling had to be taken by the orchestrator. Expected: a decision_request blocks the asker's *own* question, not the member; other threads' questions still reach it (or the request is non-blocking with the answer delivered as a message). Evidence: workspace_get team at ~00:50Z.

## WASH-R34 (2026-10-10) — An ephemeral member's completion arrived two minutes after the member had retired

b49-implementer (lifetime ephemeral, one task) committed its fix (wp-B49 80df38d36), wrote .wash/local/B49-report.md with full gate evidence, and retired; the orchestrator received its earlier FYI (`instruction` type, 01:24Z) but no `result` for the assignment, and a later message_send to it failed "recipient is not an active workspace member". The plan showed B49 `active`. Correction after the fact: the result did arrive (created 01:33:28Z, dispatched 01:35:24Z, in a batch with the reviewer's verdict on it), after the member had retired and after the orchestrator had already re-derived the state from the branch and the report and assigned the review. Delivery lagged the retirement by about two minutes. Expected: a result sent in the retiring turn is delivered before, or together with, the retirement; a message_send to a just-retired member says so ('retired at ...'), not 'not an active workspace member'.

## WASH-R30 — capability:"reviewer" members cannot report: every Wash call is refused (2026-10-14)

Status: open. Severity: blocks the review loop.

Workspace 7bce76dc, orchestrator session in mode "default" (host_auto_approval true), two
reviewers launched with `capability:"reviewer"` (applied enforcement "unverified" on
claude-agent-acp 0.90.0). Both finished their reviews and then had `member_update`
(assignment_results, waiting), `assignment_update complete` and `message_send progress` all
answered "User refused permission to run tool" — four to six refusals each, no human at a prompt.
Their turns ended with the verdicts only in their transcript text; the supervisor reported
"idle with work" every 5 min; `member_control configure configs:{mode:"auto"}` is refused:
"reviewer capability cannot override permission mode". The `about` text says reviewers "never
inherit" the launcher's approval and cannot be `auto`, so in a non-interactive session there is
no path by which a capability reviewer can file a result. Workaround used: the orchestrator read
the verdicts from `~/.claude/projects/<proj>/<session_id>.jsonl`, filed them under `.wash/local`,
ended the members, and relaunched the reviewers without the capability flag (approval auto,
read-only by instruction). Expected: the scoped Wash coordination tools (member_update,
message_send, assignment_update on own assignments) are always approved for a reviewer; they
are the one write path the role needs.

## 2026-10-19 (orchestrator session, trains 58-72)

- WASH-R35: a member waiting on its own `decision_request` is sealed: queued instructions (16), `member_control interrupt` ("no turn running"), `resume` and `checkpoint` (its priority message) never reached architect-2 for ~20 h; the only exit was `end` + relaunch, losing the queue. A decision wait should still deliver the orchestrator's instructions and a checkpoint, or the orchestrator should be able to withdraw the request.
- WASH-R36: after `member_control resume` of a stopped member (architect-5, launched as model fable), its settings showed model claude-opus-4-8; the resume did not keep the launch's model.
- WASH-R37: `plan_accept` is refused while the implementer's own launch assignment is open, so an accept always waits for the implementer's completion even when the review is done and the orchestrator holds the verdict; a reviewed node with only the implementer's assignment open should be acceptable, or the error should name which assignment.
- WASH-R38: an ephemeral implementer retires the moment its assignment completes, so every post-review fold (nine this session) fell to the orchestrator or needed a fixer launch; a short grace window after completion, or a "hold for review" flag on the launch, would keep the implementer for its own folds.
- WASH-R39: a reviewer's "Merge verdict" message often arrives before its assignment completion (seconds to minutes), so the first `plan_accept` is refused and must be retried; delivering the completion first, or atomically with the message, would remove the retry.
- WASH-R40: `workspace_configure` refuses a `task` on a member without a node ("a task is an assignment on a plan node"); a resident architect launched with a first task needs a bare launch plus an instruction message instead; the error could say so.

## WASH-R41 (2026-10-19)
`plan_set {"from": file}` is refused with "assignment … is open on node STEWARD9; a plan loads only with no work open" even when the file adds one new node unrelated to the open assignment. The `nodes` map upsert of the same node succeeds. Expected: a `from` file that only upserts (no deletions, no changes to nodes with open work) loads; or the error names the upsert form as the way. Note: `nodes` takes a map keyed by id, not an array (first attempt: "cannot unmarshal array").

## WASH-R42 (2026-10-20)
`plan_set {"nodes": {...}}` (the map upsert) accepts a title/body change on an ACTIVE node (WRITE1, WAKE1 with implementers on them) when the orchestrator calls it; the Architect's rule "never plan_set a started node" is a convention, not enforced. Expected: either enforce (refuse with the assignment id) or document that the orchestrator may correct an active node's prose. Used deliberately to retitle WRITE1/WAKE1 after their rulings corrected the premise.
