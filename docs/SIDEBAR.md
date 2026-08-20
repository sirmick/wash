# The sidebar — host-aware by relocation

Status: **plan of record** (2026-08-19). This document supersedes
[REMOTE.md](REMOTE.md) §6.2, which specified the sidebar's multi-host story as
"merge every widget's state and actions over B's RouterClient". That approach is
still correct for *awareness* and wrong for *control*; §3 explains why, and the
milestones re-cut the work along that seam.

Related: [REMOTE.md](REMOTE.md) §6 (the sidebar's place in the remote design),
§7 (the machine-half / presentation-half service doctrine this leans on), §10
(priv prompt provenance — M4 changes its premise), [AGENT_APP.md](AGENT_APP.md)
(`com.wash.ai`, which M2 grows into the agent control surface),
[ARCHITECTURE.md](ARCHITECTURE.md) (app vs chrome).

---

## 1. The defect, precisely

Under `wash remote`, some right-rail actions address the remote host and some
silently address the local one. It is not arbitrary — it falls along one API
seam.

The shell exposes two ways to reach an app, and only one carries origin:

| Call | Origin-aware? | Why |
|---|---|---|
| `sendAppMsg(instanceID, data)` | **yes** | instance ids are origin-tagged; resolves the owning client (`web/shell/src/main.tsx:1625`) |
| `sendAppMsgTo(recipient, data)` | **no** | uses the bare local `conn` unconditionally (`main.tsx:1633`); `WashRecipient` is `{app_id}｜{instance_id}` — no origin field exists to pass |

`web/shell/src/wash-app-display.ts:507` states it outright: *"sendAppMsgTo would
always go over the local connection."*

Every sidebar widget is on the wrong side of that seam, and **twice over**:

1. the FE sends to its own local session instance —
   `sendAppMsg(props.instance, …)` (`apps/session/fe/src/main.tsx:968`);
2. the session BE then does `SendAppMsgTo(wire.Recipient{AppID: …})`
   (`apps/session/be/app.go`, every `register*Gateway`), which resolves within
   its own router only.

So the binding to A is doubled, and neither hop was a design choice about hosts.
The gateway exists purely for **sender attestation** — shell-originated cross-app
sends carry no router-attested `From`, so services reject them (`app.go:90-104`).
Local-only is a side effect.

**Of eleven rail sections, exactly one is host-aware** (Remote/Hosts, because
listing hosts is its whole job). Clipboard and Viewport are local-only *by
design* and correct — the seat owns those. The other eight are wrong under
remote.

### 1.1 Notify is worse than merely unimplemented

B's toasts already reach A. Toasts broadcast to every attached shell via
`shellList()` (`internal/router/app_session.go:668`), and A's shell **is** an
attached shell on B. The frames arrive and are then deliberately discarded:

```js
case 'notify': {
  // Remote-host notifications merge into the tray in M4; for now only
  // the local router's toasts show.
  if (!isLocal) break;
```
`web/shell/src/main.tsx:450`

One guard stands between the current state and remote toasts.

### 1.2 Agents was never designed for this at all

REMOTE.md §6.2's disposition table covers "all 8 widgets". Agents, Link and
Remote post-date it. Agents therefore has **no** multi-host disposition, no merge
story, and nothing tracking that it lacks one — which is why it is the sharpest
symptom: run an agent in a remote terminal and the rail shows A's agentd only,
so roster, elapsed clock, resume/fork, detach/cancel/stop and answer-from-desktop
all address the wrong host.

Mitigating: agent approvals have a second surface *inside* the session UI
(`web/lib/src/agent-session.tsx`), which composites over from B. A remote agent
is **not** hard-blocked on an approval you cannot give. The rail is lost, not the
agent. Do not prioritise this as data-loss-grade.

---

## 2. Why not simply do REMOTE.md §6.2

§6.2 says A's session FE should subscribe directly to B's singletons over the B
`RouterClient` and merge. Two problems.

**Attestation has no answer on B.** The gateway exists because a shell-originated
send has no attested `From`. On B there is no session app to gateway through — B
runs `--no-session` (`internal/runner/router/router.go:183`). So "A's FE
subscribes to B's services" needs a mechanism that does not exist yet, for every
verb of every widget.

**It re-solves a solved problem.** wash already has cross-host addressing —
`launchOn(origin, appID)`, origin-tagged WM intents, per-origin raw channels —
but only for *windows*. §6.2 proposes a second, parallel addressing tier for
chrome, covering every control verb, and keeps it in sync forever.

---

## 3. The principle: apps are host-portable, chrome is seat-bound

Anything expressed as an **app** inherits cross-host addressing for free, plus
correct attestation (an app talking to its own host's service has a real `From`,
so its gateway can be *deleted* rather than ported). Anything expressed as
session-FE **chrome** must re-solve both from scratch.

But chrome is uniquely good at one thing an app can never do: telling you
something needs attention **without having been opened**. An app that must be
open to notify you is not a notification system.

So the seam to cut along is not *which apps produce this event*. It is:

- **Awareness** — a count, a state, a host tag: "2 agents on build01, 1 waiting
  on you." Stays in the rail. Must become host-aware, but this is a much smaller
  surface than the control verbs are: subscribe + read, no writes.
- **Control** — resume/fork/stop, approve/reject, cancel: moves in-app, where
  host-addressing already works. The rail deep-links via
  `launchOn(origin, appID)`.

**The rail stops being a control surface and becomes a router of attention.** It
answers "what needs me, and where", then hands off to something that already
knows how to live on a host.

### 3.1 Revised per-widget disposition (supersedes REMOTE.md §6.2)

| Widget | Control | Awareness in rail | Notes |
|---|---|---|---|
| **Agents** | → `com.wash.ai` (M2) | running count + pending-ask count, per host | app already exists as a one-session window; grows a roster pane |
| **Bulk** | → `com.wash.fm` (M3) | active jobs + aggregate progress, per host | **only** consumer is `apps/fm/be/upload.go` — single-app today |
| **Priv** | → prompt-as-window-app on the requesting host (M4) | pending escalation count, per host | irreducibly cross-app on the *producing* side: `journal`, `syslogs`, `packages` |
| **Notify** | stays chrome | merged, host-tinted | cross-app by definition; transport already works (§1.1) |
| **Net** | → `com.wash.net` (M5) | per-host status | app already is the control surface |
| **About** | per-host | A's by default | a machine's identity; optional B card |
| **Audio** | deferred | — | sound exits B's speakers; needs a stream, not a widget (REMOTE.md §7) |
| **Link** | A-only | — | link health *is* the seat's connection |
| **Viewport** | A-only | — | the virtual desktop is the seat's |
| **Clipboard** | A-only | — | system clipboard is the seat's; sync is REMOTE.md §7 |
| **Remote/Hosts** | already host-aware | — | unchanged |

---

## 4. Milestones

Ordered so that each lands independently and the riskiest design work happens
after the cheap wins have validated the shape.

### M0 — remote toasts  (small, independent, do first)

Delete the `!isLocal` guard (`web/shell/src/main.tsx:450`); tag the toast with
its origin and tint by host colour (REMOTE.md §11); make the toast's
`onActivate` focus via the origin-aware `focusInstance` path so clicking a B
toast focuses B's window.

No new addressing, no new verbs. Ships remote presence immediately and is the
cheapest legibility win in the whole plan.

*Risk:* a chatty remote host can now interrupt the seat. Host tinting is the
mitigation; per-host mute is out of scope.

### M1 — the awareness channel  (load-bearing infrastructure)

Everything after this depends on the rail being able to see B without being able
to *act* on B.

**Mechanism: a shell→router ctrl verb for subscribing to a background
singleton's state.** This follows the precedent already set for exactly this
situation: `ShellLaunch` was added because B has no session BE to route a
launcher click through (`internal/router/shell_session.go:830` — *"This is the
no-session-BE launch path"*). `handleLaunch` is the template — resolve the
singleton via `resolveRecipient`, let the **router** perform the attestation
(it stamps the shell), refuse anything that is not a background-surface app.

Scope it deliberately narrow: **subscribe and receive state, no writes.** Control
is moving in-app, so the shell never needs to send a service a command. A
read-only verb is far easier to reason about in the M6 hardening review than a
general shell→service channel would be.

The session FE then merges each origin's state into the widgets, tagged by host.
The local path keeps using the existing gateway — this verb is for *remote*
origins, not a replacement for A's gateway.

*Security:* a shell subscribing to B's priv state can see pending-escalation
metadata. That is the intent (awareness), but it must be named in the M6
provenance/priv-phishing review, and the state must carry no secret material.

### M2 — Agents into `com.wash.ai`  (the proving ground)

`com.wash.ai` (`apps/ai/be/app.go:70`) is today "a window onto **one** managed
agent session" — a thin host around `<AgentSession>` that already talks to agentd
directly with correct attestation, and already carries a `HistoryPanel` with
session metadata. Promote it from one-session to **roster + session**:

- roster pane listing agentd's sessions (the rail's `AgentRow` data);
- the verbs move in: resume/fork, detach, cancel, stop, answer;
- `launchOn(B, 'com.wash.ai')` then gives B's roster with **zero** new
  addressing — this is the whole point;
- rail keeps running-count + pending-ask-count per host, deep-linking to the app.

Do this one first among the relocations: single-app, the app exists, and it is
where the pain actually is. It is also the honest test of whether
"rail as router of attention" feels right before four more widgets commit to it.

*Cost, stated plainly:* the rail's resume/fork/terminate verb set was **just**
built for GH #21. M2 reworks recent code. Not an argument against — but do not
discover it halfway.

### M3 — Bulk into `com.wash.fm`

Only consumer is `apps/fm/be/upload.go`, so this is single-app despite appearing
cross-cutting. Job list + cancel + conflict resolution move into fm (which
already renders upload progress). `BulkConflictOverlay` becomes an fm modal
rather than session-FE chrome.

*The catch that makes M1 non-optional here:* a long copy **outlives the fm
window**. Close fm and control disappears while the job runs on. Bulk's awareness
half must therefore be real — aggregate progress in the rail, deep-linking to
(or re-launching) fm on the owning host.

### M4 — Priv prompt as a window app on the requesting host

The only widget where relocation makes the **security** story simpler, which is
unusual enough to suggest it is the real design.

Producers are irreducibly cross-app (`journal`, `syslogs`, `packages`, and
background services with no window at all) — but the *prompt* is one thing, and
as a window it composites to A for free.

The payoff: REMOTE.md §10 currently requires the password be encrypted to B's
priv `be_pubkey` so A's seat cannot read it. If the prompt is **B's own window**,
the password is typed into B and never traverses A at all — the encryption dance
becomes unnecessary rather than merely correct.

Open questions to settle before coding:
- a background service with no window requests escalation — who owns the prompt
  window's lifecycle, and what focuses it?
- prompt provenance/anti-phishing: a window claiming to be a priv prompt is
  exactly what an attacker would draw. Chrome-drawn trust indicator? This is the
  reason M4 sits after M2/M3 rather than before.
- does the rail's pending-count deep-link *raise* the existing prompt, or can it
  spawn one?

### M5 — Net, About per-host

Mostly launch-addressing once M1 exists. `com.wash.net` is already the control
surface, so the widget only needs its "Configure" to launch on the right host.
About gains an optional per-host card. Audio stays deferred (REMOTE.md §7 — it
needs an audio *stream*, not a widget).

### M6 — cleanup + hardening

- delete the session BE gateways that no longer have a caller
  (`register*Gateway` in `apps/session/be/app.go`) — relocation should *remove*
  code, and if it does not, the seam was cut wrong;
- rewrite REMOTE.md §6.2 to point here; update §10 if M4 lands;
- fold the new ctrl verb into the M6 remote hardening pass already in TODO.md
  (multi-tenancy, provenance/priv-phishing review).

---

## 5. What could go wrong

**Two places must agree.** A badge in the rail and the truth in the app can
drift. `sdk.StateService` is **live-only** — subscribers get a fresh snapshot on
(re)subscribe, not a replay of what was missed while disconnected (REMOTE.md §7).
A badge that was right at subscribe time can go stale silently across an SSH
blip. Prefer deriving badges from a snapshot on every (re)attach over
incrementing counters from events.

**This shrinks the multi-host work; it does not eliminate it.** M1 is still a
cross-host subscribe. The win is that it carries counts rather than every verb of
every widget, and is read-only.

**It costs the local case to fix the remote case.** The rail is genuinely good
when everything is on one box: one glance, no windows, no clicks. Moving control
in-app makes the 90% case slightly worse to make the 10% case work at all. The
mitigation is a one-click deep-link plus room for a real UI — but it is a
regression, not a pure win, and it should be felt in M2 before M3–M5 follow.

**Merge vs follow-the-focused-host.** This plan assumes awareness is **merged and
host-tagged** (§6.2's assumption). The alternative — the rail follows whichever
host owns the focused window — is a materially different design that may suit
daily remote use better. M2 is small enough to be the experiment; settle this
before M3.

---

## 6. Testing

- **Component (Tier B):** each relocated widget's new home gets `.ctest.tsx`
  coverage mirroring the existing rail tests (`AgentsWidget.ctest.tsx`,
  `BulkWidget.ctest.tsx`, `PrivWidget.ctest.tsx`) — these move with the code
  rather than being rewritten.
- **e2e, two routers:** the `?peer=` `startRouter` fixture already used by
  `e2e/tests/remote-apps.spec.ts` is the harness. Per milestone: M0 asserts a B
  toast appears in A's tray; M1 asserts a B badge count; M2 asserts
  `launchOn(B, com.wash.ai)` shows B's roster and *not* A's.
- **Router unit:** the new ctrl verb needs the `handleLaunch` treatment —
  refuse non-background surfaces, unknown apps, protocol mismatch — alongside
  `internal/router/shell_session.go`'s existing tests.
- **The rail must be asserted local-only until it isn't:** a regression test that
  A's rail shows A's agents while a B agent runs would have caught this defect.
