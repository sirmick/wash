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

Stated as an invariant rather than a split (settled 2026-08-19, after M1):
**state mutation belongs in the app; chrome is read-only.** That is the load-
bearing version of §3, and it is worth preferring to "awareness vs control"
because it explains more:

- it is why the gateway can be *deleted* rather than ported — an app talking
  to its own host's service has a router-attested `From` already, so the
  attestation problem the gateway exists to solve stops existing;
- it is why `hostgw` is safe to expose unattested (§3.2's whole trick): there
  is no write to protect;
- it *predicts* the local-case regression in §5 instead of excusing it. Bulk's
  cancel going from one click to two is the doctrine's price, not a flaw in
  the cut. Which means the §3.2(7) tripwire is not "did control get further
  away" — it will have — but "is awareness still enough to act on."

The M2 rework cost (§M2, the GH #21 verb set) was accepted on this basis.

### 3.1 Revised per-widget disposition (supersedes REMOTE.md §6.2)

| Widget | Control | Awareness in rail | Notes |
|---|---|---|---|
| **Agents** | → `com.wash.ai` — **DONE** (M2) | running count + pending-ask count + a per-host door; asks stay answerable here (§3.2(8)) | the app grew a roster pane and the key-addressed verbs |
| **Bulk** | → `com.wash.fm` (M3) | active jobs + aggregate progress, per host | **only** consumer is `apps/fm/be/upload.go` — single-app today |
| **Priv** | → prompt-as-window-app on the requesting host (M4) | pending escalation count, per host | irreducibly cross-app on the *producing* side: `journal`, `syslogs`, `packages` |
| **Notify** | stays chrome | merged, host-tinted — **DONE** (M0/M1) | cross-app by definition; transport already works (§1.1) |
| **Net** | → `com.wash.net` (M5) | per-host status | app already is the control surface |
| **About** | per-host | A's by default | a machine's identity; optional B card |
| **Audio** | deferred | — | sound exits B's speakers; needs a stream, not a widget (REMOTE.md §7) |
| **Link** | A-only | — | link health *is* the seat's connection |
| **Viewport** | A-only | — | the virtual desktop is the seat's |
| **Clipboard** | A-only | — | system clipboard is the seat's; sync is REMOTE.md §7 |
| **Remote/Hosts** | already host-aware | — | unchanged |


### 3.2 Decisions (settled 2026-08-19, planning session)

Seven questions the first draft left open. Recorded with reasoning because two
of them look open-and-shut from the outside and are not.

1. **Merged awareness, focus-aware presentation** — not follow-the-focused-host.
   A rail whose remaining job is awareness must see all hosts at once: the
   interrupts worth surfacing are precisely the ones from hosts you are *not*
   looking at, and the focused host's state is already on screen in its own
   windows. Follow-focus re-creates the original defect with the axis flipped
   ("follows A always" becomes "follows wherever you are"). M0 also already
   merged the toast tray; a follow-focus rail under a merged tray is two
   theories of attention in one column of pixels. Follow-focus's one good idea
   survives in presentation: sections group by host, local first, remote groups
   collapsed-but-badged, auto-expanding on events (the existing
   `autoExpandSection` machinery), with the focused window's host emphasised.
2. **`hostgw` is symmetric from day one, staged.** It runs on every router —
   including A's — and owns awareness *reads* for all origins uniformly. The
   session BE gateway keeps the control verbs, which die milestone by milestone
   as control moves in-app (M6 deletes the remainder). That is not two
   implementations of one thing: each owns a different thing, and one is on a
   planned path to zero. Staged (remote-only first, local flip second) so a
   local-rail regression bisects cleanly. Until the control milestones land,
   local services simply have two subscribers (hostgw + session BE) — harmless,
   `StateService` fans to both.
3. **Multi-user B dissolves into the router boundary.** A router is per-user by
   construction: the unix listener accepts SCM_RIGHTS handoffs only from one
   SO_PEERCRED-verified uid (`--allow-uid`,
   `internal/runner/router/router.go:196`), and the remote supervisor brings
   B's router up as the SSH user. "Every attached shell" therefore means *your
   own other seats* — exactly the audience M0's toast broadcast already
   reaches via `shellList()`. `hostgw`'s privacy model **is** the router's
   attach model. What survives into M6 is an audit, not a design: confirm no
   cross-uid attach path exists (raw-router token gate, login cookie). If that
   audit ever fails, toasts already leak today; hostgw is not the new problem.
4. **Badges recompute from snapshots, never increment from events.** `hostgw`
   pushes full snapshots per (origin, service); each delivered message replaces
   that cell wholesale; everything is re-pushed on every (re)subscribe.
   Flapping origins: grey the host group on `reconnecting`, drop it on `down`
   (RemoteWidget already tracks per-host status). A greyed stale count is
   honest; a confident stale count is a lie.
5. **M2 is roster-as-second-pane** in the existing `com.wash.ai` window
   (master–detail), not a multi-instance restructuring — `HistoryPanel` is
   already half a roster. Launching from the roster may open a dedicated
   window; the default single window is roster+session. Keeps M2 additive: an
   experiment that requires restructuring first is not an experiment.
6. **M4's trust indicator comes from router metadata, never window content.**
   The shell knows a window's owning app id from `app.declared`, and the app
   cannot forge it; chrome draws "com.wash.priv @ host" in its own stripe, in
   the host hue. An attacker can render a pixel-perfect prompt *inside* a
   window; they cannot make the chrome say it is priv. This also retitles the
   plan's biggest security question: it is M4's prompt provenance, not §3.2(3)'s
   broadcast audience.
7. **Sequencing holds (M3→M4→M5), with a named tripwire after M2**: if the
   deep-link feels like a toll rather than a doorway — you keep opening
   `com.wash.ai` for things the rail used to answer in one glance — stop and
   re-cut M3–M5 toward richer rail widgets over `hostgw` instead. Written down
   now so sunk cost cannot argue past it later.

8. **Permission asks stay in the rail — a named exception** (settled
   2026-08-20, during M2c). Every other agent verb moved into
   `com.wash.ai` under §3's invariant, and answering an ask *is* a
   mutation, so by the rule it should have moved too. It didn't, and the
   reason is worth keeping: everything else that moved, you were opening
   the app for anyway — to read the transcript, to reply, to watch it
   work. Answering allow/deny is the one case where opening a window is
   pure overhead, and an agent blocked on a human is the single thing the
   rail exists to surface. One click stays one click. The ask renderer is
   therefore `AgentAsks` in `@wash/ui`, used by both the rail and the
   roster, and `agent_answer` is the one control verb the session BE
   gateway keeps.

---

## 4. Milestones

Ordered so that each lands independently and the riskiest design work happens
after the cheap wins have validated the shape.

### M0 — remote toasts  (small, independent, do first) — **DONE** 2026-08-19

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

**Mechanism: a background gateway app on every router.** B gets a small
background singleton — working name `com.wash.hostgw` — that does exactly what
A's session BE gateway does today: subscribe to its own host's background
services, and republish their state. A's shell reads B's gateway over the
connection it already holds.

Three facts already in the tree make this work with **no new wire protocol**:

1. `relayAppMsgToShell` fans an app's message to **every attached shell**
   (`internal/router/app_session.go:627`) — and A's shell *is* an attached shell
   on B. This is the mechanism REMOTE.md §7 already names as the presentation
   half: *"presentation services already broadcast to the connected shell
   (`shellList()`), and A's tunnelled shell is connected."*
2. `deliverAppMsg` compounds the incoming id with the delivering client's origin
   (`web/shell/src/main.tsx:933`), so B's state arrives already tagged as B's.
3. An **app**-originated cross-app send carries router-attested `From`
   automatically. That is the whole reason the session BE gateway exists
   (`apps/session/be/app.go:90`), and a gateway app on B inherits it for free.

So the flow is: A's shell asks B's gateway to subscribe → the gateway subscribes
to B's services as a properly attested app → each `state` push is republished to
the gateway's own FE → the router fans it to every attached shell → A's shell
receives it tagged `origin=B` and merges it into the rail.

Two small additions are needed on A's side:

- an **origin-aware `sendAppMsgTo`**. Today it always uses the local `conn`
  (`main.tsx:1633`); the remote variant picks the client by origin, exactly as
  `launchOn` already does.
- a **shell-level listener** for gateway messages. `deliverAppMsg` routes to a
  registered app element, and a windowless background app has none, so gateway
  traffic needs somewhere to land.

Scope it deliberately narrow: **subscribe and receive state, no writes.** Control
is moving in-app, so the shell never needs to send a service a command.

The local path keeps using the session BE gateway unchanged — this is for
*remote* origins. (A tidier end-state folds A's gateway into `hostgw` too, so
there is one implementation instead of two; that is an M6 cleanup, not a
prerequisite.)

#### Implementation staging

- **M1a — `hostgw` + shell plumbing, remote origins only.** New FE-less app
  `apps/hostgw/be` (`com.wash.hostgw`, surface=background, singleton,
  spawn-on-demand). Shell: instance→app-id map from `app.declared`; intercept
  hostgw traffic in `deliverAppMsg`; a cross-element Sub keyed
  (origin, service); subscribe on peer attach, re-subscribe on reattach.
  Proof: two-router e2e — B's badge state reaches A.
- **M1b — flip local awareness reads.** A's shell subscribes to A's own
  `hostgw` identically; rail badges and host groups read the hostgw map for
  LOCAL too; the widgets' interactive internals keep their legacy
  `notify.state`/`bulk.state`/… feeds until their control milestone moves them
  in-app. Do **not** delete any session BE gateway here.
- **M1c — rail presentation.** Host groups per §3.2(1): merged, local first,
  remote collapsed-but-badged, `autoExpandSection` on remote events,
  grey-on-reconnecting / drop-on-down per §3.2(4).

#### Pinned mechanics (verified 2026-08-19 — do not re-derive)

- `app.declared` fires for **every** instance, background included
  (`declareInstanceLocked`, `internal/router/shell_session.go:93`), and a
  late-connecting shell is told about already-running instances
  (`internal/router/router.go:1766`). The declare carries the manifest, so the
  shell can map instance→app id. Route hostgw traffic on that map, **never**
  on payload shape (a payload-shaped route is spoofable by any app).
- The shell-side intercept must happen in `deliverAppMsg`
  (`web/shell/src/main.tsx:932`) **before** `deliverToInstance`:
  `deliverToInstance` parks messages for unmounted elements in
  `pendingMessages` (`web/shell/src/api.ts:186`), and hostgw has no element —
  mis-routed pushes would queue unboundedly.
- Shell→session-FE surface: a cross-element Sub plus a `window.wash` accessor —
  the exact `windowsSub` / `linkStats` pattern the session chrome already
  consumes (`apps/session/fe/src/main.tsx:319`).
- The shell's first subscribe addresses `{app_id: "com.wash.hostgw"}` over the
  owning origin's conn; `handleAppMsgSend` resolves via `resolveRecipient`,
  which spawns the singleton on demand. No new ctrl verb.
- `hostgw` accepts **unattested** subscribes from shells deliberately: it is
  read-only, so there is nothing to protect the verb with. The asymmetry is
  the whole trick — services demand attestation, and hostgw provides it by
  *being an app*; it is the attestation boundary.
- State stays `any` end to end, mirroring `serviceStateMsg`
  (`apps/session/be/app.go:448`). Never `json.RawMessage`/`[]byte` — the
  router base64-encodes byte strings.
- Service set and naming mirror `serviceFEKind` (`apps/session/be/app.go:168`):
  the same services the session gateways subscribe to today. Envelope:
  `{kind:"hostgw.state", service:"notify"|"bulk"|"priv"|…, state:<verbatim>}`.
- FE-less service app checklist: add to `SVC_APPS` (`Makefile:68`), rerun
  `gen-pkg-binaries` (`packaging/wash.binaries` is guarded by
  `check-pkg-binaries`), `gen-imports`, and remember the `.PHONY` hazard for
  FE-less Go binaries.
- `hostgw` subscribing at startup spawns its host's services on first shell
  attach — parity with what A's session BE already does at session start.
  Accepted cost.

#### Why not a shell→router ctrl verb

The obvious alternative — a `ShellSubscribe` verb modelled on `ShellLaunch`,
letting the router subscribe on the shell's behalf — was designed and rejected
during M0. `ShellLaunch` is a fire-and-forget *command*; a subscription needs a
**return path**, and that is where it falls apart. `StateService` keys its
subscribers by `from.InstanceID` and pushes with
`SendAppMsgTo(wire.Recipient{InstanceID: …})` (`pkg/sdk/stateservice.go`). A
shell is not an app instance, so the router would have to mint pseudo-instances
for shells and intercept `resolveRecipient` to redirect them — surgery on the
router's addressing core, to reach a place a normal app already stands.

The gateway app also generalises: one subscriber serves every widget, so M3 and
M5 add a state shape rather than new plumbing each.

*Security:* two things for the M6 provenance/priv-phishing review. A shell
subscribing to B's priv state can see pending-escalation metadata — that is the
intent (awareness), but the state must carry no secret material. And `hostgw`
republishes to **every** shell attached to B, which on a multi-user B is a wider
audience than a targeted push would be; the pseudo-instance design could have
targeted one shell, and this one cannot. Gate `hostgw`'s state on the same
ownership rule that governs which shells may attach at all.

#### As built (2026-08-19) — corrections to the pinned mechanics

The pinned facts held, with one exception and two clarifications worth
carrying into M2+.

**Wrong: M1a cannot be "remote origins only" at the data layer.**
`EnsureBackgroundAppsRunning` spawns **every** registered background app
on **every** shell connect (`internal/router/autoboot.go:71`), so A's own
`hostgw` boots unprompted and its republishes land under `local` before
anything asks. The staging still bisects cleanly, because what M1a
changed is what *arrives*, and M1b is what changed what the rail
**reads** — but "remote-only" describes the reads, not the channel.

The subscribe is still load-bearing, for a different reason than assumed:
autoboot covers the *first* fan-out only. On a page refresh the gateway is
already running and has no reason to re-push, so without an explicit
catch-up the new shell sees nothing until state happens to change. Same
argument for a reconnect. `Conn.onState` fires on every transition to
`open`, so one `installHostgwSubscriber` per client covers first attach
and every redial — `reconcileRemoteAttachments` needed no hook.

**Sharper: `subscribe` must be `sdk.HandleVoid`, not `HandleFromVoid`.**
The plan said hostgw accepts unattested subscribes; the trap is that
`HandleFromVoid`'s handler returns early when `from` is nil
(`pkg/sdk/bus.go`), and the router relays a shell's cross-app send as a
plain `EvtAppMsg` with no sender (`handleAppMsgSend`,
`internal/router/shell_session.go:811`). Getting this wrong drops every
shell subscribe **silently** — no error, no log. Relatedly, hostgw must
**not** install `sdk.NewStateService`: that registers `subscribe` itself,
and `Bus.register` panics on a duplicate kind.

**Also true:** `serviceFEKind` covers seven services; hostgw watches six.
`com.wash.remote` is excluded — the Hosts widget is already host-aware,
and B republishing its own host list says nothing true about B's place in
A's desktop. `audio` is watched but deliberately unread: sound exits B's
speakers, so per-host audio needs a stream, not a badge (REMOTE.md §7).

**Cost noted for the §3.2(7) tripwire:** M1c's host hue is a fourth copy
of the palette+hash collapsed back to a second (the rail's own module and
the shell's). An app FE bundle cannot import the shell's, so the shell's
user overrides (`setHostColor`) are invisible to the rail — a pinned
colour shows in the window stripe and not in the group. Folding host hues
into a shell-exposed accessor is M6's.

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

Decided shape (§3.2(5)): a second pane in the existing window — master–detail
— not a multi-instance restructuring.

Do this one first among the relocations: single-app, the app exists, and it is
where the pain actually is. It is also the honest test of whether
"rail as router of attention" feels right before four more widgets commit to it.

*Cost, stated plainly:* the rail's resume/fork/terminate verb set was **just**
built for GH #21. M2 reworks recent code. Not an argument against — but do not
discover it halfway.

#### As built (2026-08-20)

Landed as M2a (roster pane), M2b (verbs), M2c (rail down to awareness).
Four things the build found that the plan did not say:

- **`agentd` publishes more per row than the rail ever read** — mode,
  modes, yolo, configs, commands, used, size. The app had been
  redeclaring a near-duplicate row type to get at them; sharing the
  renderer forced them onto one type.
- **A door keyed on "waiting" is the wrong door.** The first cut offered
  a way into a host only when something there was blocked on you, so a
  host with an agent working away was unreachable. Watching or stopping a
  working agent is a reason to open the app; waiting still wins the
  label, but it is not the entry condition. Caught by the two-router e2e,
  which is the point of having one.
- **Detaching your own session closes its window**, so the way back to a
  detached session is the rail's door → the app → its roster. That makes
  the deep-link load-bearing rather than a convenience, and it is now the
  path `agent-roster-verbs.spec.ts` walks.
- **Reattaching from a roster spawns a second window**, leaving the one
  you opened to find the roster sitting empty behind it. Minor, and the
  §3.2(5) note ("launching from the roster may open a dedicated window")
  anticipated the shape — but it is the kind of friction the §3.2(7)
  tripwire is watching for, so it is written down rather than shrugged
  off.

The tripwire itself is not yet answerable: it asks how the deep-link
feels in daily use, which needs daily use. What can be said is that the
glance survived — the rail still answers "2 waiting on build01" without
opening anything (M1c), so what the door costs is the price of *acting*,
not of *knowing*.

### M3 — Bulk into `com.wash.fm`

Only consumer is `apps/fm/be/upload.go`, so this is single-app despite appearing
cross-cutting. Job list + cancel + conflict resolution move into fm (which
already renders upload progress). `BulkConflictOverlay` becomes an fm modal
rather than session-FE chrome.

*The catch that makes M1 non-optional here:* a long copy **outlives the fm
window**. Close fm and control disappears while the job runs on. Bulk's awareness
half must therefore be real — aggregate progress in the rail, deep-linking to
(or re-launching) fm on the owning host.

How the outlive case resolves (settled 2026-08-19): fm's Jobs surface binds to
the **bulk service singleton**, not to the window's own operations — the job
never belonged to the window (that is why closing fm doesn't kill a copy
today). Any fm window on host X lists and can cancel *all* of X's jobs,
including ones whose originating window is gone. Chain: job outlives fm → rail
badge persists (hostgw feeds it; awareness never depended on fm) → click →
`launchOn(origin, com.wash.fm)` → cancel in the jobs panel. Cancel costs two
clicks where the rail's X cost one — a real local-case regression, and exactly
the class of cost the §3.2(7) tripwire judges; bulk is the first candidate for
a re-cut if it fails.

A conflict firing with **no fm open** stalls that item and raises a
notification (merged + host-tinted since M0); activating it opens fm on the
owning host with the conflict modal up. This needs one small shell affordance:
**toast activation must fall back from focus-the-instance to
launch-the-owning-app** — the bulk service has no window to focus. Any fm on
the host may answer; `resolve_conflict` is idempotent by job id, first answer
wins.

### M4 — Priv prompt as a window app on the requesting host

The only widget where relocation makes the **security** story simpler, which is
unusual enough to suggest it is the real design.

Producers are irreducibly cross-app (`journal`, `syslogs`, `packages`, and
background services with no window at all) — but the *prompt* is one thing, and
as a window it composites to A for free.

The payoff needs stating carefully — the first draft of this section
overclaimed it. A wash app's FE runs **in the seat's browser**: a "window on B"
is B's BE plus B's bundle executing in A's page, so the password field is still
DOM in A and the submitted secret still transits the relay. What the window-app
move actually buys is that REMOTE.md §10's encryption stops being a
**cross-component protocol** (session chrome handling a foreign service's
pubkey) and becomes an implementation detail **inside one app** — priv's own FE
encrypts to its own BE's key, shipped with the ask. Same wire protection, no
key distribution across trust boundaries, and the prompt's provenance is
chrome-attested (§3.2(6)). Simpler, not unnecessary.

Two further properties (settled 2026-08-19):

- **The prompt app is priv's face**, exactly as wash-connect is the face of
  `com.wash.remote` (REMOTE.md §6.1's precedent): pending asks, the
  granted-apps list, revoke. The producers — `journal`, `syslogs`, `packages`,
  windowless services — change **not at all**; priv's cross-app ask API is
  untouched, and the multi-app-ness stays behind it. Only where the ask is
  *rendered* moves.
- **Prompts are user-summoned, never self-opening.** The ask surfaces as a
  rail badge + toast (both chrome-owned); the window opens only when the user
  clicks through, via plain `launchOn(origin, …)` — no new BE-spawns-window
  path. This is an anti-phishing property in itself: the real priv prompt
  structurally *cannot* appear unbidden, so a "priv prompt" that does is by
  definition fake.

Decided (§3.2(6)): the trust indicator is chrome-drawn from `app.declared`
metadata — never from window content.

Remaining questions to settle before coding (lifecycle and raise-vs-spawn are
answered by the summon rule above):
- the trust stripe's visual design — how chrome renders "com.wash.priv @ host"
  so it reads as chrome and cannot be mistaken for window content;
- ask expiry: what happens to an escalation that is never summoned (timeout
  semantics, and what the requesting app sees);
- multi-seat races: two of the user's seats summon the same ask — first answer
  wins, but the second window must degrade honestly.

### M5 — Net, About per-host

Mostly launch-addressing once M1 exists. `com.wash.net` is already the control
surface, so the widget only needs its "Configure" to launch on the right host.
About gains an optional per-host card. Audio stays deferred (REMOTE.md §7 — it
needs an audio *stream*, not a widget).

### M6 — cleanup + hardening

- delete the session BE gateways that no longer have a caller
  (`register*Gateway` in `apps/session/be/app.go`) — relocation should *remove*
  code, and if it does not, the seam was cut wrong;
- fold A's remaining gateway into `hostgw` so there is one implementation of
  "subscribe to this host's services" rather than two that must agree;
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

**Merge vs follow-the-focused-host — SETTLED (§3.2(1)): merged data,
focus-aware presentation.** The M2 tripwire (§3.2(7)) is the remaining guard:
if the awareness/control split feels wrong in daily use, re-cut M3–M5 rather
than pushing four more widgets through it.

---

## 6. Testing

- **Component (Tier B):** each relocated widget's new home gets `.ctest.tsx`
  coverage mirroring the existing rail tests (`AgentsWidget.ctest.tsx`,
  `BulkWidget.ctest.tsx`, `PrivWidget.ctest.tsx`) — these move with the code
  rather than being rewritten.
- **e2e, two routers:** the `?peer=` `startRouter` fixture already used by
  `e2e/tests/remote-apps.spec.ts` is the harness. Per milestone: M0 asserts a B
  toast appears in A's tray; M1 asserts a B badge count reaching A's rail through `hostgw`; M2 asserts
  `launchOn(B, com.wash.ai)` shows B's roster and *not* A's.
- **Go unit:** `hostgw` gets the service-gateway treatment — it is a normal app,
  so it tests like one (`apps/session/be/gateway_test.go` is the model). The
  interesting case is republish fan-out: a state push must reach a shell that
  attached *after* the subscribe.
- **The rail must be asserted local-only until it isn't:** a regression test that
  A's rail shows A's agents while a B agent runs would have caught this defect.
