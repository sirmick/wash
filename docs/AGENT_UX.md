# Agent UX — one mental model, every door opens

Status: **phase Now shipped** (2026-08-21). Grounded audit of the agent
feature's navigation layer + a two-phase plan. Phase "Now" (§5) is
polish-release scope and is **done** — see §5 for what each item became;
phase "Next" (§6) now has that doc — [AGENT_MESSENGER.md](AGENT_MESSENGER.md)
— and two of its bullets shipped early with 0.14.0; §6 says which.

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

## 3. Audit (as of 2026-08-21, v0.13.2+51 — the state phase Now fixed)

Kept as written, in the present tense of the day it was made: it is the
record of what was wrong, and §5 says what each line became.

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

## 5. Phase Now — polish-release scope (shipped)

- **N1 — focus-or-launch, the one primitive.** ✅
  `web/shell/src/focus-or-launch.ts` is the pure decision (raise this
  app's window on that host; cycle when there are several; return null =
  launch), exposed as `window.wash.focusOrLaunch`. Wired into the rail's
  Agent door, the host-group rows (which gained an optional ↗), and the
  windowless-toast fallback. The *keyed* half turned out to belong in
  agentd rather than the shell: only the service knows whether a session
  has a window, lost one, or never had one, so the desktop hands the key
  back (`wash.focus`) and agentd raises or opens. That also gave the
  History panel's live rows a verb — they were inert (§ below).
- **N2 — agentd toasts needs-input.** ✅ One toast at the single point
  where a question actually reaches a human (never on a re-arm, never on
  the paths that deferred without showing anything), carrying the session
  key. `wire.EvtNotify.Key` / `ShellNotify.Key` are opaque to router and
  shell and only ever handed back to the app that raised the toast, which
  is what makes them safe. Terminal-tier asks are deliberately unkeyed
  until wash-term can answer `wash.focus`; they keep the generic
  fallback, which opens the Agent app with the question visible.
- **N3 — badge the hidden-sidebar chevron.** ✅ The 14px tab carries the
  merged waiting count and says so in its tooltip.
- **N4 — single-click reattach.** ✅ Dropped the dblclick guard; agentd's
  `claimDetached` was always the real double-spawn guard and is atomic
  (`TestClaimDetachedAllowsOnlyOneReattach`), so nothing needed changing
  service-side.
- **N5 — launcher defaults.** ✅
  - **N5a:** the agent select opens on the first *available* adapter,
    preferring `claude`; the submit fallback uses the same kernel.
  - **N5b:** last-used agent + folder win over the static preference,
    read from the session history agentd already persists and publishes
    as `recent` — no new store, so nothing can disagree with it.
- **N6 — taskbar attention.** ✅ New `EvtWindowAttention` +
  `SessionWindow.Attention`: the app raises the flag, the *router* clears
  it on focus, so no app can leave a pill pulsing at a window you have
  read. wash-ai sets it while its session is blocked; the pill ORs it
  with the pre-existing unread-notification dot.

Tests: pure kernels under `node --test`, component tests for the rail and
roster, Go tests for the attention state machine and the toast rules, and
`e2e/tests/agent-focus.spec.ts` for N1+N2 end to end (both halves — FE
window state and BE router log).

## 6. Phase Next — 0.15, messenger consolidation

**Now has its design doc: [AGENT_MESSENGER.md](AGENT_MESSENGER.md)**
(2026-08-24). What that doc settles, and how this section's bullets
turned out:

- Roster as the app's permanent pane — **shipped early, in 0.14.0**. It
  is always on screen with a `<Splitter>` divider, its width persisted
  per window; the "▶ Roster" toggle and its three auto-open rules are
  gone. This bullet was overtaken by a direct request during testing.
- Launcher defaults (agent + folder) — **shipped**, as N5. "Recent
  sessions inline" did not: it becomes part of the list merge.
- Sessions-not-windows, history merged into the list, and one status
  vocabulary — **open**, and the substance of AGENT_MESSENGER.md's M1–M5.

The mechanism finding that shaped that doc, recorded here because it
contradicts this section's original assumption: `Instancing` is honoured
on the `shell.launch` path and **ignored** by `EvtSpawnRequest`, which is
what the start menu and all three of agentd's window-opening paths use.
"Change `Instancing` for com.wash.ai" would therefore have changed only
the doors, which already behave.

## 7. Non-goals

- No seeded permission policy, no auto-answer changes — ASK M1–M4 stand.
- No fork verb until the honest replay exists (HISTORY's reasoning).
- No cross-host roster merging in the app — the app shows its own host;
  the rail is where hosts merge (SIDEBAR.md §3.1). N1's cross-host reach
  is `launchOn`, nothing new.
