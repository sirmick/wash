# Session prompt — a question nobody answered is not a "no"

Goal, in one line: **a permission prompt must never become a denial just
because time passed**, and if it does, the agent and the human must both be
told why.

Paste this as the opening prompt for a dedicated session. It is self-contained.
This doc **owns `apps/agentd/be/ask.go`**. `docs/PROMPT-session-lifetime.md` M3
touches `askTTL` in passing for a different reason (whether a `needs-input` row
should pin a session alive); coordinate before both branches edit this file.

---

## Reported symptom

> When I first open agent, at least under claude, it seems to auto-reject
> everything when it's set to manual.

Reproduced from logs. It is not a rejection — it is a **30-second fuse on every
question, where running out is reported to the agent as `cancelled`**.

## The chain (confirmed)

```go
// apps/agentd/be/ask.go:36,206,233
const askTTL = 30 * time.Second
p.timer = time.AfterFunc(askTTL, func() { expireAsk(id) })
_ = p.reply(DecisionDefer, "timeout")
```

`DecisionDefer` then matches neither arm of the switch in `RequestPermission`
and falls off the end:

```go
// apps/agentd/be/acp.go:496-504
case d := <-answer:
    switch d {
    case DecisionAllow: return pick(req.Options, acp.OptionAllowOnce, …), nil
    case DecisionDeny:  return pick(req.Options, acp.OptionRejectOnce, …), nil
    }
    return acp.Cancelled(), nil     // ← every unanswered question lands here
```

The agent receives `cancelled`, which is **indistinguishable from the user
hitting cancel**. On the Claude side this surfaces as `Tool use aborted`.

Evidence, `router-s-394818dc.log`, 2026-08-20 06:29–06:33: **ten asks, ten
expiries, zero decisions.** `ask-1` … `ask-10`, every one `after=30s`, no
`acp decide` line anywhere until the user gave up and switched the rows to
yolo/auto at 06:40.

## Why "everything", and why on first open

There is no `~/.config/wash/agents.json` on the reporting machine
(`internal/agentpolicy/agentpolicy.go:72-74` for the path). With no policy file
`agentpolicy.Evaluate` matches nothing, so **every** tool call falls through to
asking a human. Rules only accumulate as the user clicks "always".

So a first session has zero rules and asks about literally everything — which is
exactly when a 30-second-per-question budget is least survivable. It gets better
with use, which is the wrong direction: the moment of maximum friction is the
user's first impression.

## The design problem

Compare the two automatic outcomes:

- **Auto-approve** narrates itself every time. `acp.go:433-441` appends
  `"Auto-approved (yolo): …"` to the transcript, with a comment stating that
  "an agent that is being auto-approved must not look like one that is being
  watched."
- **Auto-reject** narrates nothing. No transcript line, no UI trace, no log on
  the agent's side of the boundary. The tool simply fails.

The loud path is narrated and the silent one is not.

There is also a lesson here the codebase already learned once, on the path
immediately above. `acp.go:448-458`:

> **Asking is the floor for a hosted session, not an opt-in.** The terminal tier
> could safely decline to answer because deferring returned control to an agent
> with its own prompt in a pty. A hosted session has no such UI — wash IS the UI
> — so declining means the tool silently never runs and the turn ends. Observed
> on the first real Codex session: `decision=defer reason="policy off"` followed
> immediately by `state=done`, with nothing on screen to explain it.

That is this bug, diagnosed and fixed for the *policy-off* defer, while the
*timeout* defer still produces the identical outcome. `DecisionDefer` is
load-bearing in a tier that has nowhere to defer to.

## Ruled out (do not re-chase)

- **`maxPendingPerRow = 3`** (`ask.go:41`) rejects a 4th concurrent ask instantly
  with `DecisionDefer`/"too many pending" — a genuine instant-reject path, and a
  plausible story for an agent that emits parallel tool calls. It is **not** what
  happened: `grep -c too-many-pending` is **0** across all three session logs.
  Still worth fixing eventually, since a parallel-calling agent will hit it.
- **`ask_desktop off`** (`acp.go:460-465`) returns `Cancelled()` immediately, but
  is gated on `pol.Enabled`, and `AskDesktopOrDefault` returns false when there
  is no policy at all (`agentpolicy.go:65-70`). With no `agents.json` this branch
  is unreachable.
- **The in-window ask UI.** It is complete and correct: `AskRow` with allow /
  allow-always / deny, plus `a`/`d` keyboard shortcuts bound to the oldest
  pending ask (`web/lib/src/agent-session.tsx:238-338`, rendered at :515).

## What is NOT established

**Why nobody answered.** There is no evidence the prompt failed to render, and
the UI above looks right. Two candidates, neither confirmed:

1. The user was not looking at that surface. Asks also render in the rail
   (`apps/session/fe/src/main.tsx:1388-1398`, a named exception to the SIDEBAR
   §3.2(8) relocation) and the rail auto-expands its agents section on a new ask
   (`:978`) — but that is a different pane from the agent window.
2. **A window can only show its own session's asks.** `main.tsx:208` filters
   `a.row_key === sessionKey()`, and `AgentSession` is gated on `sessionKey()`
   (`:637`). Two live sessions were running on 2026-08-20 and the log shows asks
   alternating between `acp:1` and `acp:2`, so whichever window was in front was
   structurally incapable of showing half of them.

Establish which before building M1 — a repro with one session and one window
open, agent in manual mode, is the cheap experiment.

---

## The work order

### M1 — timeout must not silently mean cancelled

Split the outcomes. `DecisionDefer`-by-timeout is not `DecisionDeny` and not a
user cancel, and all three currently collapse into `acp.Cancelled()`.

- Give the agent something it can distinguish, so it can report "nobody answered
  this in 30s" rather than failing opaquely. Check what the ACP spec allows here
  before inventing a shape — `reject_once` is a lie (the user rejected nothing)
  and `cancelled` is the status quo.
- **Narrate it in the transcript**, mirroring the yolo line at `acp.go:433-441`.
  An agent whose tools are being auto-denied must not look like one whose tools
  are failing.

### M2 — do not run the clock while nobody can answer

Pause or extend `askTTL` when no viewer is attached to the row. A wall-clock
`time.AfterFunc` guarantees that a question asked while the user is away is
denied before they can possibly see it.

**Coordinate with `docs/PROMPT-session-lifetime.md` M3**, which reaches the same
constant from the disconnect side. One change, one branch.

### M3 — a window should not hide asks it cannot show

If the `row_key === sessionKey()` filter is confirmed as a contributor, the agent
window needs to surface the existence of asks belonging to other rows — a count,
a jump-to affordance — rather than silently omitting them. The roster pane
already renders all of them (`main.tsx:523`), but defaults to closed and only
auto-opens at `rows().length > 1` (`:177`, `:194`).

### M4 — first run asks about everything

With no `agents.json` every call asks. Consider what a sane first-run baseline
looks like: a seeded policy, a first-run affordance that offers "allow reads in
this directory" as one click, or simply a longer TTL until the user has any
rules at all. The goal is that the first session is not the worst one.

## Traps

- **Do not fix this by lengthening `askTTL` alone.** A longer fuse is still a
  fuse; the defect is that expiry is silent and indistinguishable from cancel.
  Fix the reporting first, then tune the number.
- **Do not remove the timeout.** `RequestPermission` blocks the agent for its
  whole duration (`acp.go:410-412`), and `hostedAskTTL = askTTL + 5s`
  (`acp.go:37-40`) exists so exactly one deadline fires. An unbounded wait wedges
  the turn instead of failing it.
- `DecisionDefer` has three distinct producers — timeout, too-many-pending, and
  policy-off. They mean different things and currently share one code path.
  Whatever M1 lands should keep them distinguishable.
