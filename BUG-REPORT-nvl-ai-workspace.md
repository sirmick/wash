# Wash issues observed during NVL-AI reconciliation

Owner request: keep a running report of Wash failures encountered in this
workspace. Record evidence and workarounds; do not treat application failures
or expected permission denials as Wash bugs without evidence. No credentials.

Workspace: NVL-AI · MR reconciliation → production
Project: `/home/mcloonan/nvl-ai-dev/mr-reconciliation`
Observed server: Wash `0.16.0`, workspace API `4.0.0`.

Workspace instances (entries below belong to the instance live at their date):

| Instance | Workspace ID | Notes |
|---|---|---|
| 1 | `030097895eba4f106b7905b11fb35992` | Original discovery session; WASH-001…003 observed here. Ended. |
| 2 | `4360b981ac2257e79a513f9edbfe12a7` | Reopened 2026-09-28 from `.wash/plan.toml` + `.wash/qa`. Plan revision and the open `m1-pagination-proof` thread both restored intact; no members staffed. |

Instance 2 note: the supervisor is currently **disabled** (`supervisor.off`, workspace
revision 3). With no members staffed it repeated the two `reported` M1 nodes every
5 minutes with nothing actionable. This is a deliberate orchestrator setting, not a
Wash failure, and is recorded here only so that an absence of watchdog alerts in this
instance is not mistaken for evidence of healthy members. Re-enable when staffing.

## WASH-001 — Advertised subagent restriction fails on Codex launch

Date: 2026-09-28
Status: open; operational workaround applied. No Wash code changed.
Category: capability discovery / configuration validation / launch UX.
Impact: medium; two discovery workers failed to start on first attempt.

### Reproduction

1. Call `workspace_get` with `view: "about"`.
2. Create a workspace and plan nodes.
3. Configure Codex members with valid name, role, instructions, lifetime, node,
   task, and `subagents: "deny"`, as exposed in `workspace_configure`.
4. Inspect the returned `launches` results.

Both `baseline-auditor` and `mr-cartographer` failed with:

```text
subagents "deny" unsupported by codex; no session started
```

The outer MCP result had `isError: false`; member definitions were committed
and members remained in `failed` state. This partial-commit behavior is
documented by Wash, so it is not itself claimed to be a separate defect.

### Expected / observed mismatch

The tool advertises `subagents: "allow" | "deny"` and describes `deny` as
removing the member's subagent tool. The about guide provides explicit
provider-support caveats for reviewer capability, but did not expose an
equivalent support matrix for `subagents`. It was not evident from discovery
that `deny` was unavailable with the current Codex provider.

Desired behavior: make unsupported combinations discoverable before launch,
preferably reject them in preview/configuration validation, and document the
provider support matrix. Do not silently claim enforcement when unavailable.

### Workaround and outcome

Reconfigured the same member keys without `subagents`, retained instructions
not to spawn agents, and set `can_spawn: false` for Wash spawning authority.
Both launched successfully; plan nodes became active with working members.
Instructions are not equivalent to provider-enforced removal of all subagent
tools; `can_spawn: false` concerns Wash authority only.

### Evidence

- Failed configuration request:
  `nvl-ai-reconciliation-discovery-team-20260928`.
- Successful correction:
  `nvl-ai-reconciliation-discovery-team-corrected-20260928`.
- Baseline member ID: `96b4bdafbab77d8700e55d2cdef51fd4`.
- Inventory member ID: `6c8b60f4e79c1e7f322f12bc996c84af`.
- Failure observed once for each member; successful workaround verified via
  launch results and `plan_get`. No further reproductions attempted.

### Suggested regression coverage

- Discovery exposes provider-specific subagent-control support.
- Preview catches unsupported provider/subagents combinations without launch.
- Configuring a valid replacement for a failed member retries successfully
  without duplicate assignments or lost tasks.

## WASH-002 — Baseline worker reported stalled with queued instruction

Date: 2026-09-28
Status: investigating; suspected runtime/adapter or activity-reporting issue,
not yet established as a Wash defect.
Impact: baseline discovery delayed; no production or application mutation.

### Observation

Supervisor lifecycle message `3c5e03c2a0d37a3eab18941e34e524fa` reported:

```text
wedged: Production baseline and release gates (baseline-auditor) has been
in a turn for 2m5s with nothing from it (no output, no tool, no busy process);
1 message(s) queued for it.
```

Member: `96b4bdafbab77d8700e55d2cdef51fd4`.
Session: `01a0e97a-3c63-7df0-b07f-857ad436d16f`.
Active assignment: `4182b73870c475d3fd021863fafc689b`.
The evidence directory had no baseline report when inspected. The other
discovery worker was still showing tool activity.

### Recovery and limits of evidence

Followed the supervisor instruction with `member_control interrupt`; the tool
returned `interrupted: true`. The immediately following plan still showed the
member working and its original assignment active. That snapshot alone does
not establish failed cancellation: asynchronous delivery may still be pending.
Sent instruction `baseline-stall-recovery-20260928` to continue the same
assignment, first report any blocker, and save a partial local-evidence report.
No duplicate assignment or replacement member was created.

The interrupt did not restore observable progress: a second supervisor message,
`24fdaa33133f247447f9739fff1364db`, reported another 2m2s silent turn with one
queued message. No baseline report existed at the second inspection.
Ended the original member; Wash confirmed it ended and marked its assignment
cancelled (not complete). Created an orchestrator-authored recovery handoff at
`/home/mcloonan/nvl-ai-dev/mr-reconciliation/evidence/baseline-recovery-handoff.md`
and launched `baseline-recovery` on the same node, with instructions to save
partial local evidence early and report access gaps promptly.

### Replacement follow-up: report progress despite another stall alert

Supervisor message `73dac825dd6111a6b7de9a922ea99510` reported the replacement
`baseline-recovery` silent for 2m3s (no output/tool/busy process). Interrupt again
returned `interrupted: true`. Inspection immediately afterwards found a
substantively expanded `evidence/baseline.md`, with repository, runtime, CI,
historical production and release-gate findings, and a conclusion that the
bounded report was complete. Core local SHAs/runtime IDs/health were independently
spot-checked by the orchestrator and matched.

Therefore this replacement alert is **not evidence that no work was done**.
It may flag a long reasoning or finalization gap after saved progress. Asked
the worker to report its last action and finalize the original assignment.
The replacement subsequently completed assignment `02207fa4edc74f1accee746e31762582`
and returned the frozen report in result `21607353739fac9022073359fc0b69fa`.
The recovery therefore produced usable evidence. In answer
`9d28667ed40835064bddfc01e6fbf0d0`, the replacement reports that its last research
action was a completed bounded command reading the runbook and checking report
headings/line count. It was finalizing/reasoning toward its result, not waiting
for a tool or blocked on access. Docker read permission had succeeded and
GitLab MCP was accessible. The interrupt arrived before its intended final
member_update; the reminder turn delivered completion successfully.

For this replacement, evidence supports a **watchdog silence alert during
finalization**, not a proven tool/service deadlock or lost work. The original
worker's cause remains unknown. The watchdog did wake the orchestrator correctly.

Determine whether this was provider silence,
long unreported reasoning, stale activity tracking, or delivery/cancellation
failure before attributing root cause. An unproductive turn is not proof of
an application deadlock.

## WASH-003 — Inventory watchdog alert with five queued instructions

Date: 2026-09-28. Status: investigating, not a proven Wash defect.
Supervisor message `109fe476d6c92bd4025b87dd9c809768` reported MR inventory member
`6c8b60f4e79c1e7f322f12bc996c84af` silent for 2m3s with five queued messages.
Earlier context warning `5778ca6a06451a2335a0abdec91c6f7d` was at 61%; handoff
instructions had been sent, but session usage later reached about 70% before
the final handoff was returned. Those messages had not all been delivered.

By inspection, the member had saved substantive `mr-inventory.json` (~398 KiB)
and `mr-inventory.md` (~61 KiB); no evidence of lost findings. Interrupted the
member (tool returned `interrupted: true`) and requested immediate handoff and
bounded-assignment completion without more broad queries. Independent review
of the saved reports was launched separately.

The inventory worker subsequently completed assignment
`7e2a26893d1fed857c8450bd66610b0a` in result
`583e4521ccf999ad34bfa87724c754c2`, freezing both reports for review. No findings
were reported lost; a replacement inventory session is unnecessary now that
the bounded work is complete. Its explanation of the silent interval is not
yet available. Investigate whether queued
handoff instructions cannot steer an active long reasoning turn soon enough,
or whether this is expected delivery behavior plus an overly short silence
threshold. Do not treat normal context warnings or queued messages alone as
bugs; include provider/activity timestamps before concluding.

## WASH-004 — Assignment `body` not delivered to the member; title only

Date: 2026-09-28. Instance 2 (`4360b981ac2257e79a513f9edbfe12a7`). Wash 0.16.0,
API 4.0.0. Status: **suspected**, one clear reproduction, cause not established.
Impact: medium. A member received a one-line title with no instructions and
correctly stopped to ask; the round trip cost a few minutes. A less careful
member might have guessed at the scope.

### Observation

`assignment_update` with `action: create`, `member_id`, `node`, `text` (a
one-line title) and `body` (about 2 KB of instructions), request_id
`assign-orphans-and-defectB`, created assignment `fa72ef9ac12585de3ae796dac4c8062d`
for member `352f73cac75280a7cb24971d475bd469`. The member replied (message
`9bf82473b71b0121c57a3b86d9b97238`): "arrived as a title only ... There is no
body". The orchestrator resent the same content with `message_send`
(`resend-lost-assignment-bodies`), and it was received.

### Possibly related, weaker evidence

Earlier, a `member_update` QA `resolve` on thread `m3-openwebui-rebase-shape`
opened its body with "Proceed with A". The thread creator replied (message
`76fc1db202138741ccef46ec8b61f61f`) that the resolution notice "restates the
finding but does not say which option was decided". So the leading line may have
been dropped, or only a summary delivered. Not confirmed as the same defect.

### Unknowns before calling it a bug

- Whether `body` on create is meant to be member-visible at all, or whether
  `text` is the only delivered field. If so, this is a documentation/UX gap: the
  schema accepts `body` silently, and nothing indicates it will not be shown.
- Whether earlier assignments in this instance lost their bodies too. Several
  members acted on details that were also present in their launch
  `instructions`, so their behaviour cannot distinguish the two cases.
- Size: this body was near the 2000-byte guideline. Truncation or rejection at
  a limit has not been ruled out.

### Workaround

Put the full instructions in `message_send` with the `assignment_id`. That path
has delivered reliably in this instance. Where the scope matters, have the
member confirm what it received.

### Suggested check

Create an assignment with a short, distinctive `body`, then read the member's
delivered view (inbox or workspace state). Repeat at around 500 and 1900 bytes.

## WASH-005 — Instructions to an idle member on a completed assignment never wake it

Date: 2026-09-28. Instance 2. Wash 0.16.0. Status: **observed once, clear
symptoms**; the cause (by design or a defect) is not established.
Impact: low-medium. A follow-up sat undelivered for about 9 minutes. The
supervisor then reported the member as possibly stuck, which it was not.

### Observation

Member `b9c0bc86fb352dde6deb02f0df103da2` completed assignment
`53eda5ad745956c2dce2209488626eab` and went idle. The orchestrator then sent
two `instruction` messages carrying that assignment_id (`9b3d67be…`, `15dc9b50…`).
The tool description says instructions "wake idle members". They stayed queued.
The supervisor then reported: "2 message(s) … queued for 8m48s and it is not in
a turn. Its session may be stuck".

- `member_control interrupt` → `interrupted: false, reason: "no turn running"`.
- `message_retry` → "only uncertain messages can be retried".
- `member_control resume` → the undelivered count cleared, but the member stayed
  idle and did not act on the content.
- Creating a new assignment woke it.

### Likely cause, unconfirmed

Messages tied to an assignment that is already completed may be delivered into
that closed context without starting a turn. If this is intended, the tool
docs should say so, and the supervisor should not describe the member as
stuck.

### Workaround

For follow-up work after a member reports, create a NEW assignment and put the
instruction in its one-line `text` (see WASH-004: `body` may not be delivered).
Send further detail with `message_send` against that new assignment's id.

## Future entries

Append a numbered issue with timestamp/version, expected vs actual behavior,
minimal reproduction, sanitized error, impact, workaround, and verification.
Separate confirmed failures from suspected causes. Preserve prior entries.
