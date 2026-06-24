# wash — TODO / backlog

One consolidated, actionable list. Detailed design/rationale lives in the
`docs/` audit docs (linked per section); this file is the short "what's
left" view. Items are grouped by area, roughly high-value first.

Last consolidated: 2026-06-23 (pruned completed items after a full
unit-test + e2e-test green gate).

---

## Security  (docs/CORE_AUDIT.md §1)

- [ ] **1.1 — verify wash-login's HMAC session cookie on the raw router.**
  Defense-in-depth: the router-token gate already blocks anonymous LAN
  access, so this is the remaining belt-and-suspenders — when the router
  is *not* fronted by wash-login, verify the `internal/login/cookie.go`
  HMAC cookie on `/ws`, `/screenshot`, `/app/`.
- [ ] **1.4 CSP — blocking Content-Security-Policy on the shell.** Headers
  (X-Frame-Options / nosniff / Referrer-Policy) shipped; a real CSP is
  deferred because the shell loads xterm, CodeMirror and Webamp, each
  needing inline/worker/blob allowances. Needs in-browser verification
  before it can block (start `default-src 'self'`, expect some
  `style-src 'unsafe-inline'`).

## Backend structural debt  (docs/TECH_DEBT.md P2, docs/CORE_AUDIT.md §3)

- [ ] **`internal/sdk/bus.go` struct→`map[string]any`→struct round-trip.**
  The BE↔FE decode path marshals to a map and back; collapse to a typed
  path. Architectural — touches every app's message decode.
- [ ] **2.4 `bus.Emit` swallow annotations.** ~14 bare `_ = bus.Emit(...)`
  sites; either an `EmitLogged` helper or per-site "safe to drop" comments.
  Low value, annotation-only.

## Frontend  (docs/FE_REFACTOR_PLAN.md, docs/TECH_DEBT.md)

- [ ] **FE monolith slimming (Phase 5).** `apps/fm/fe/src/main.tsx` (~3.8k
  lines) and `edit` (~3.4k) — slim `App` to wiring + a `view/` split. The
  shared logic already moved to `@wash/fs-client`; this is the remaining
  big one. Its own dedicated effort.
- [ ] **`createEditState` (Phase 6).** Apply the state+controller split to
  `edit` (it shares the package but keeps an inline store).
- [ ] **(optional) Phase 7** — same playbook for `session` / `top`.

## wash-fm

- [ ] **Expand-folder scroll anchoring.** Expanding a folder inserts rows
  above the viewport so content shifts down; scroll should compensate for
  the added length so the clicked row "stays where it is" visually.
  - Investigated (2026-06-23): the jump only reproduces with the browser's
    native `overflow-anchor` disabled — **Chromium/Firefox already pin the
    content; only WebKit/Safari (no native scroll anchoring) jumps.** A JS
    polyfill (capture topmost-visible row before a re-list, restore its
    offset after) fought Solid's async `<For>` render timing + the
    multiple-`list_ok` ordering of a single fs change and proved fragile —
    backed out. Revisit (likely via a robust observer-based anchor, or just
    accept native-only) if Safari/iPad becomes a supported target.

## Test suite hygiene  (docs/TECH_DEBT.md P3)

- [ ] **8-worker suite timing-race flakes** (fm-be / net-vm / music /
  settings) papered over by `retries:1` + a 12s control-socket timeout —
  root-cause properly. (See memory: e2e load flakes.)
- [ ] **Sidebar e2e order.** 3 fm-related specs pass alone but fail in the
  full suite after M7; not root-caused.

## pty / channel handling  (structural)

- [ ] **Local-term channel-ownership race on peer churn.** Connecting/
  disconnecting a remote host (or rapid browser open/close) can break an
  already-open *local* terminal: raw PTY frames get dropped
  (`drop raw frame on channel N (owned by another shell)`) or control
  messages land on a vanished instance (`shell app_msg: dropping message for
  unknown instance=i-N`), and the shell's `/bin/bash` can end up killed.
  Root: `reattachChannelsToShell` (internal/router/shell_session.go) assigns
  channel ownership to the *latest* shell; an overlapping/older session keeps
  ownership and the new session's frames are dropped. Surfaced repeatedly in
  cluster testing of wash-connect (auto-reconnect cut the frequency by
  reducing peer churn, but did not fix the race). See memory: wash dev loop.
  Same root as **RECONNECT-AUDIT.md A4** ("raw channels bind to exactly one
  `b.shell`; reattach claims only detached bindings") — filed there as
  deliberate v1 single-shell; a remote peer attach is effectively that
  "second shell", so it now bites in normal use, not just with two tabs.
- [ ] **Structural fix for wedged ptys (design).** A single dropped/misrouted
  frame currently wedges a terminal with no recovery path. Options to weigh:
  an authoritative pty registry (channel → owning shell/instance, looked up
  on every frame instead of cached, so a stale owner can't strand frames); a
  pty supervisor that detects a wedged channel and re-binds or restarts it;
  and/or making channel ownership a lease the newest shell renews rather than
  a one-shot reassignment. Goal: a term never silently goes black —
  it either keeps working or visibly recovers.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

- Hand-rolled insertion sorts (`cmd/wash/main.go`, `runtime_stats.go`) —
  intentional, avoids importing `sort` for two lines.
- `fm-replace.spec.ts` symlink test asserts `existsSync` only — by design.
- CORE_AUDIT 2.3 divergence traps (sparkline, Overlay screen-scope, token
  subset) — deliberate, see docs/CORE_AUDIT.md §2.
- `wash new-app` scaffold — deferred until a new app actually needs it.
