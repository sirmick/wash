# wash Control Bus — humans and AI co-drive the desktop

**Status: design only, unbuilt.** This supersedes the settle/dispatch
assumptions in `docs/AGENT.md` (which remains the tier-philosophy doc).

One bus through which **humans and AI co-drive every app and window**, and
which doubles as the **e2e harness at scale**. The existing Playwright +
per-test-router + `waitForLog` setup (`e2e/fixtures/router.ts`) is already a
working prototype of it.

## 1. Shape

**Read = one unified representation.** A shell-level walker produces a lean
node tree (YAML) of every interactive element across all apps. Each node:
stable identity + a cheap inline **summary** + the **actions** on it + typed
**links**. Heavy/large detail (full FM file lists, editor tab contents) is
**not** inlined — nodes export links that resolve to BE intents on demand
(progressive disclosure). This is structurally MCP: links = resources,
verbs = tools.

This works because all apps render **light-DOM as sibling custom elements in
one shell document** — no Shadow DOM, no iframes — and the shell keeps a live
`instanceID → element` registry (`web/shell/src/api.ts`).

**Write = three deliberately separate mechanisms, by where the action enters
the stack:**

| Mechanism | Enters at | What it is | Status |
|---|---|---|---|
| **Signal** | back (BE direct, bypass FE) | drive app logic headless; existing `id`-correlated `controlMsg` | ~built |
| **Semantic** | middle (→FE, normal FE→BE flow) | faithful "as a user"; what co-drive + true e2e need | net-new |
| **Pixel** | front (screenshot + raw input) | **raw wlroots surfaces only** (last resort) | ~80% built |

DOM-window pixel capture is **out of scope** — no CDP/`getDisplayMedia` in the
production browser, synthetic events are `isTrusted=false`. DOM windows fall
back to the semantic mechanism.

## 2. Synchronous actions via an action DB (not ambient `action_id`)

Every action blocks until its causal chain settles, then returns the fresh
view. Settle is tracked by an **action database**, not by propagating an
ambient token (which is unsound across the goroutine/`await` boundaries the
SDK mandates).

- A participant doing **load-bearing** work takes a **lease** (`begin`) and
  clears it (`end`) on completion — from FE or BE, any process.
- The handler author *declares* what's on the completion path. **Side effects
  (toasts, telemetry) never lease** — which is exactly why the notify-toast
  fan-out that defeated the counter approach is a non-issue here.
- Settle = no open leases for the action + the FE render-stable (itself just a
  "render pending" lease the FE clears after rAF/MutationObserver quiesce).
- **Default: auto-lease the SDK reply path** (begin on dispatch, end on
  reply-echo) so the common synchronous case is free; only explicitly-async
  derived work opts into manual `begin`/`end`.

**Contract: `settled | timed_out | errored` — best-effort, never
causal-complete.** Residual hazards stay honest: register-before-clear
ordering discipline (a findable bug, not an impossibility), and leaked leases
(crash/disconnect) GC'd by a bounded timeout / lease TTL, reaped on instance
exit.

**One store, three uses.** The action DB, the annotation blackboard, and the
existing `app_state` map are the *same* pattern — a router-owned keyed store
mirrored to the FE over `SessionPatch` (`internal/router/wmstate.go`). Build it
once. Provenance (`origin: human | agent:X`) is just a column on the action
row; "what is action X waiting on?" is a query (debugging + co-drive
visibility + eval assertions, free).

## 3. Annotations / blackboard

An action can bundle annotation writes ("click that **and** set annotation =
XYZ"), bundled + synchronous, return includes them. Bus-owned overlay store
keyed by **durable** refs. Doubles as co-drive visibility (shell renders a
badge / AI-cursor label) and multi-agent coordination (claims). Caveat: atomic
with action **emit**, not **settle**.

## 4. Shell as root

The shell chrome is bus participant #0 and the root of the node tree. WM verbs
(`window.move/raise/focus/minimize/maximize/tile/close`) are ~6/7 already
wired (`wmstate.go` + `window.wash`). Net-new: `window.tile`, and hoisting
`start.open` / `sidebar.toggle` out of the session app's private Solid signals.

## 5. Co-drive

Humans + AI share the same input paths; the AI moves a **visible real cursor**;
per-window **arbitration lock** held across the full settle (built once,
shared). Auth = **accident-prevention + provenance + kill-switch**, *not* a
security boundary — on a single-user 0600-socket box an AI at the same uid
bypasses any per-intent gate, so a policy layer there is theater. Reuse
wash-priv's approval-queue/audit for destructive-action gates.

## 6. The keystone: a stable semantic-path scheme

Five workstreams (perception, annotations, settle, co-drive, shell-verbs)
depend on a stable per-node identity that survives Solid re-render. It does not
exist — today's only intra-app handle is a **mutable** testid
(`fm-entry-${name}`, breaks on rename mid-action). **This must be assigned an
owner before anything downstream starts.**

## 7. Phasing (~13–15 eng-months total)

- **M0 — Foundations (~3wk).** The semantic-path scheme + the structured
  intent stream (`action_id`/`origin`/`kind` through the 3 dispatch switches).
  *Unlocks everything.*
- **M1 — Read + Signal + CLI = thinnest credible v1 (~7wk).** Perception
  walker + signals + bus CLI (extend `wash-launch`) + thin MCP read/signal
  adapter. **Can:** read every window's tree, headless actions, scripting.
  **Cannot:** as-a-user actions, settle, co-drive, pixel, element-level
  annotations.
- **M2 — Action-DB settle + shell/WM verbs (~13wk).**
- **M3 — Co-drive + annotations (~9wk).**
- **M4 — Raw-surface pixel + eval-at-scale (~8wk).**

## 8. Top risks

1. Settle is best-effort, never causal-complete — **must be sold as such.**
2. The semantic-path keystone is unowned and gates five workstreams.
3. Per-intent auth is theater on a same-uid box — decide threat model up front.
4. DOM-window pixel is undeliverable — raw surfaces only.
5. Eval determinism inherits settle's residual flakiness (harness already needs
   `retries:1` at 8 workers).

## 9. Decisions to settle before building

1. Settle contract = best-effort + timeout. **(decided ✓)**
2. Auth = accident-prevention + provenance + kill-switch. **(decided ✓)**
3. Pixel = raw surfaces only; DOM → semantic. **(decided ✓)**
4. Owner of the semantic-path scheme — **open.**
5. The leasing convention (auto-lease reply path; side effects don't; async
   opts in) against the real `Handle`/`Mutate`/notify paths — **open.**
6. Where chrome state (`start.open`/`sidebar.toggle`) lives — **open.**
7. Single owner for the thrice-claimed shared primitives (arbitration lock,
   AI-cursor overlay) — **open.**
