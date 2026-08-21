# Agent UX — one mental model, every door opens

Status: **plan of record** (2026-08-21). Grounded audit of the agent
feature's navigation layer + a two-phase plan. Phase "Now" (§5) is
polish-release scope; phase "Next" (§6) is a 0.15 design effort that gets
its own doc before any code. N5a (launcher default agent) landed with this
doc; everything else is open.

Related: [SIDEBAR.md](SIDEBAR.md) (the relocation that put the roster in
`com.wash.ai`), [AGENT_APP.md](AGENT_APP.md) (the app's contract),
[AGENT_TERM.md](AGENT_TERM.md) (term-embedded agents; their toast path is
the one that already works), TODO.md §Agent UX.

---

## 1. The problem: three half-models

Ask "what is the agent feature?" and the code answers three ways at once:

1. **A window is a session.** `com.wash.ai` is `InstancingMulti`, one
   window per conversation, "the taskbar is still the window switcher"
   (apps/ai/be/app.go header).
2. **A window is a viewer onto any session.** The roster pane's
   single-click re-points *this* window's detail at the clicked row
   (`select`, apps/ai/fe/src/main.tsx), so windows and sessions are in
   fact decoupled.
3. **The rail is the real roster.** Counts, asks, doors, per-host groups
   (SIDEBAR.md M2c).

Each is defensible; together they are incoherent — a user cannot predict
what a click does because the design has not decided what the noun is.

**Decision: the agent app is a messenger, not a terminal.** Sessions
outlive windows (LIFETIME), carry blocked/unread state (ASK), are
searchable history (HISTORY), and span hosts (SIDEBAR). That is a
conversation list, not a tab strip. The tabbed-xterm instinct is right
about the *shape* — a list of live things plus one focused view — but the
messenger frame names it correctly and imports every user's existing
expectation, the load-bearing one being: **clicking a conversation never
opens a duplicate.**

What does NOT change: permission asks stay answerable in the rail
(SIDEBAR.md §3.2(8)) — answering yes/no is the one case where opening a
window is pure overhead. And the rail's awareness-only doctrine survives,
*clarified*: it forbids chrome from **mutating** app state, and focusing
or launching a window mutates nothing. Navigation is not control.

## 2. Use cases, ranked

- **UC1 — "Something needs me."** An ask lands or a run finishes. The
  session's window may be focused, buried, minimized, absent; the sidebar
  may be hidden; the work may be on another host. Every one of those
  states must offer one gesture that lands the user at the question.
- **UC2 — "Where are my agents?"** N sessions, M hosts: who's working,
  who's blocked, who's done — without opening anything.
- **UC3 — "Go to that one."** Glance → conversation. One click, no
  duplicates, works cross-host.
- **UC4 — "Start one."** From the rail, from inside the app, from a
  terminal, on host B.
- **UC5 — "Pick up where I left off."** Reload, reconnect, the morning
  after, a session the router outlived.
- **UC6 — "Run several / compare."** Two sessions side by side. The only
  case that genuinely wants multi-window; must stay possible, must not be
  the default outcome of ordinary clicks.

## 3. Audit (as of 2026-08-21, v0.13.2+51)

| Entry point | Today | Expected | Verdict |
|---|---|---|---|
| Toast: agentd session needs input | **No toast exists** — agentd never notifies; the rail section auto-expands only if the sidebar is open | Toast on needs-input; click lands at the ask | Gap (UC1) |
| Toast: term-embedded agent | EvtNotify → `focusInstance` → the terminal window | same | OK — the model to copy |
| Toast fallback for windowless sender | `notifyOpeners` maps only `bulk→fm` (web/shell/src/main.tsx); an agentd toast would launch the headless service | Open `com.wash.ai` **at that session** | Trap for the UC1 fix |
| Rail host-group rows | Deliberately inert ("no buttons", HostGroups.tsx) | Click → that host's workspace | Doctrine over-applied; see §1 |
| Rail "Open Agent" door | Always `launchOn` → **new window per click** | Focus-or-launch | Defect (UC3) |
| Roster row click, attached elsewhere | `select` re-points this window → two views of one session, unasked | Focus the owning window; attach here only if none owns it | Wrong default; keep as explicit pop-out (UC6) |
| Roster row, detached | Single-click dead; dblclick reattaches (double-spawn guard) | Single-click, like every row | Make reattach idempotent instead of hiding it |
| "▶ Roster" toggle | Text button + ask-badge only when closed | — | Disclosure widget dressed as an action; dissolves in §6 |
| Sidebar hidden | The 14px chevron tab carries **no badge** | Chevron shows waiting count | Gap — last rung of the interrupt ladder (UC1) |
| Taskbar | Ordinary pills; no needs-input attention; windowless sessions absent | Pill flashes/badges on needs-input | Gap (UC1/UC2) |
| Launcher | Two-field form, agent starts on "Choose…"; submit silently falls back to **first available = codex** (table order) | A home: sensible default agent, recents, history | §5 N5 + §6 |

## 4. The interrupt ladder (doctrine)

Attention escalates through exactly one ladder, every rung clickable, each
click landing at the ask via the same primitive (N1):

    transcript view → taskbar pill flash → rail badge/row → toast → modal

Modal is priv-only (SIDEBAR.md M4). A rung is skipped only when a higher
one is already on screen. agentd's ask TTL already re-arms while no
desktop could answer (ASK M2) — the machine side is honest; this ladder is
the human side.

## 5. Phase Now — polish-release scope

- **N1 — focus-or-launch, the one primitive.** Shell-side helper: given
  `(origin, sessionKey?)` → focus the window attached to that session,
  else re-use an existing `com.wash.ai` window on that origin (select),
  else `launchOn`. Wire into: the AgentOpen door, host-group rows (make
  clickable), roster activation, and N2's toasts. Kills the duplicate-
  window class entirely.
- **N2 — agentd toasts needs-input.** Once per ask (respecting
  `ask_desktop` off), body = truncated question, carrying the session
  key; `notifyOpeners['com.wash.agentd'] = 'com.wash.ai'`; activation
  routes through N1. Term-embedded agents keep their existing path.
- **N3 — badge the hidden-sidebar chevron** with the merged waiting count
  (same number as the Agents section badge).
- **N4 — single-click reattach.** Make `row_reattach` idempotent in
  agentd (a second reattach for an already-attaching session is a no-op,
  not a second window), then drop the dblclick guard in agent-roster.
- **N5 — launcher defaults.**
  - **N5a (done, landed with this doc):** default the agent select to
    the first *available* adapter, preferring `claude`; the submit
    fallback uses the same kernel (apps/ai/fe/src/default-agent.ts).
  - **N5b:** remember last-used agent + folder per user. BE-owned via the
    existing persist seam (`persistSessionView`,
    docs pattern: BE-owned FE view-state) — last-used beats the static
    preference when both are available.
- **N6 — taskbar attention.** needs-input sets the window's
  attention/urgency flag → pill flash, cleared on focus. (If the wire has
  no attention bit yet, that is the work.)

N1 is the keystone; N2/N3/N6 are rungs of §4; N4/N5 are affordance fixes.
Each lands with component tests; N1+N2 get a full-stack e2e (test app +
Playwright + router-log assertions) since they cross shell↔app↔service.

## 6. Phase Next — 0.15, messenger consolidation (design doc first)

Not in this release. A SIDEBAR-grade design doc settles, at minimum:

- Roster becomes the app's permanent, collapsible left pane — the app's
  spine, not a flap. The "▶ Roster" toggle dissolves into the standard
  pane chevron.
- Sessions-not-windows as the noun: default one workspace window per
  host; explicit "open in new window" pop-out serves UC6. This changes
  `Instancing` semantics for `com.wash.ai` and must reconcile with
  window-restore and `launchOn`.
- Launcher becomes the home surface: agent + folder (defaulted per N5),
  recent sessions inline, history search as the same list's past — one
  search box governing live and stored.
- History panel merges into the conversation list (the "unfinished"
  distinction survives as a row state).
- Status vocabulary at every size: waiting = accent + pulse, working =
  neutral spinner, done = dim check, stale = grey — legible at rail,
  roster, and taskbar sizes.

## 7. Non-goals

- No seeded permission policy, no auto-answer changes — ASK M1–M4 stand.
- No fork verb until the honest replay exists (HISTORY's reasoning).
- No cross-host roster merging in the app — the app shows its own host;
  the rail is where hosts merge (SIDEBAR.md §3.1). N1's cross-host reach
  is `launchOn`, nothing new.
