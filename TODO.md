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

## Multi-user / ingress sharp edges  (wash-login)

- [ ] **vscode (and other `/app/` ingress apps) don't self-heal after a
  wash-login restart → spurious "unauthorized".** Every `wash-login` restart
  (notably each estate package upgrade) tears down user sessions *and* the
  per-launch code-server **ingress token**. The wash shell's `/ws` reconnects
  and re-auths, so window apps (fm/term/…) recover transparently — but the
  **vscode-workbench iframe** still points at the now-dead `/app/<token>/`,
  which wash-login answers with **401 unauthorized** (session gone) or the
  router with **410 Gone** (stale token). Symptom: vscode shows "unauthorized"
  while every other app is fine, until you re-open the app or re-login.
  Ingress apps affected: vscode/vscode-workbench, music, radio, washamp.
  - Fix: the workbench (`apps/vscode-workbench/fe/src/main.tsx`) should treat a
    401/410 from its `/app/` iframe as "ingress died" and auto re-call `ensure`
    (re-mint the token / relaunch code-server), instead of leaving a broken
    iframe. The 401 path is `internal/login/server.go` `/app/` →
    `identityFromRequest` fail; the 410 path is the router's stale-token branch.
  - Diagnosed live on carrier-dev 2026-06-24: server side is healthy
    (`--auth none` on a unix socket, managed binary) — purely a stale
    client-side token/session after the 0.9.4→0.9.5 rollout restarts. Not a
    port/socket collision. See [[wash-login-ingress]], [[wash-debug-carrier-dev]].

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

## pty / channel handling  (structural)  — DONE (docs/PTY_ROBUST.md)

The terminal-hang surface is closed (merged via wash-pty-robust). Kept here
only as a pointer; full design + rationale in docs/PTY_ROBUST.md.
- **Local-term ownership race** → Fix A: the foreground head shell
  authoritatively owns every non-peer terminal channel; a stale/zombie owner
  can no longer black-hole input (supersedes the RECONNECT-AUDIT A4 band-aid).
- **Wedged ptys** → Fix B: terminal output forwards non-blocking, so a wedged
  FE never back-pressures into the child shell; suppressed output is held
  byte-exact in the ring and recovered by a `channel.resync` (FE term.reset +
  mode re-seed + realigned snapshot) — no torn stream, no silent black.
- **Dead-but-open client** → Fix C/D: a per-write `wsWriteTimeout` stops one
  wedged client hanging every terminal on a shell; teardown + the existing FE
  reconnect path give visible recovery.
- Deliberately deferred (documented): a periodic router-wide resync sweep
  (only covers an FE that drains but never grants credit — an FE bug).

## 2026-07-01 correctness-review leftovers (out of scope for the fix pass)

The reliability/data-path/display fix pass (Phases A–D) landed the confirmed
high/medium bugs. These lower-priority items from the same three reviews were
explicitly deferred; each references the review doc + finding id.

Data plane (REVIEW-DATAPATH.md):
- [ ] **F9** — `wire.ReadLoop` can leak its reader goroutine at teardown
  (select the buffered send against a `done` channel closed on return).
- [ ] **F10** — a max-size (16 MiB) B-frame overflows the peer relay's A-frame
  wrapper; split oversized relay payloads or cap B producers at `MaxPayload−8`.
- [ ] **F11** — notes: `ChannelCredit.Reserve` single-waiter wakeup constraint;
  `DecodeFrameRaw` not covered by the frame fuzzer; `Scheduler.TrySubmit`
  enqueues after `Close`.

Reconnect (REVIEW-RECONNECT.md):
- [ ] **M5** — deferred `mountWhenReady` can resurrect a router-deleted window
  as an unclosable ghost (per-origin snapshot epoch).
- [ ] **M6** — a self-closed relay socket wedges a remote client forever (treat
  a non-detach relay-socket close as fatal for the origin → detach + re-attach).
- [ ] **M7** — post-confirm window close SIGTERMs with no escalation →
  permanently unopenable app (arm a grace→`Process.Kill()` ladder).
- [ ] **L2** — queued input silently dropped on `unauthenticated` (emit
  `lost-input`); multi-tab head-steal is silent (wants a UI affordance).
- [ ] **L3** — teardown gated on `cmd.Wait` while a grandchild holds the stdout
  pipe (use `cmd.WaitDelay` / don't gate teardown on Wait).
- [ ] **B1 deferred half** — move `handleAssetRead` streaming off the shell
  dispatch loop (`TODO(review F5)` in `shell_session.go`); the read-side
  liveness stamp already prevents the false idle-reap.
- [ ] Smaller notes list (5523ef3 residual focus gaps; login first-spawn flock
  doesn't re-`List`; bundles re-shipped every reconnect; …).

Display / X11-Wayland (REVIEW-X11-WAYLAND.md) — protocol-coverage gaps:
- [ ] wl_data_device **drag-and-drop** (in-app DnD dead), **primary selection**
  (middle-click paste), **wp_viewporter**, **presentation-time**,
  **xdg-activation**, **min/max size**, Xwayland **-auth** (cross-uid X access;
  pair with the D4 runtime-dir work), **request_minimize/set_app_id/icons**.
- [ ] **#13** — keymap hard-coded to the server default (non-US host layouts
  type wrong chars; needs a layout hint from the FE); `handle_set_selection`
  reader thread can block forever if the selection owner exits without writing.
- [ ] **#5 unresolved** — the popover classifier over-matches an UNTITLED GTK
  message dialog (renders it as a pointer-grabbing overlay, not a movable
  window). The review's proposed discriminators (no xdg-decoration / no min-max
  / recent-pointer) each break real Qt menus and were reverted; needs a signal
  the toolkits don't expose here. A TITLED dialog already stays a window. Plus
  nested serial-less submenu chaining (`TODO` in `toplevel_setup_popover`).

Out of scope entirely: VP9/WebRTC transport work.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

- Hand-rolled insertion sorts (`cmd/wash/main.go`, `runtime_stats.go`) —
  intentional, avoids importing `sort` for two lines.
- `fm-replace.spec.ts` symlink test asserts `existsSync` only — by design.
- CORE_AUDIT 2.3 divergence traps (sparkline, Overlay screen-scope, token
  subset) — deliberate, see docs/CORE_AUDIT.md §2.
- `wash new-app` scaffold — deferred until a new app actually needs it.
