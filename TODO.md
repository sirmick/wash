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

Data plane (REVIEW-DATAPATH.md): **ALL FIXED — Phase E, merge dbce705.**
- [x] **F9** — `wire.ReadLoop` reader-goroutine leak: the buffered send now
  selects against a `done` channel closed on return (80f8fbb).
- [x] **F10** — max-size B-frame on the peer relay: oversized frames are now
  split across relay frames (pieces sent at ClassControl so the strict-priority
  scheduler can't interleave them; FE `send` mirrors the split) (99b53ab).
- [x] **F11** — `DecodeFrameRaw` cross-checked in `FuzzDecodeFrame`;
  `Scheduler.TrySubmit`/`SubmitTelemetry` no-op after `Close`;
  `ChannelCredit.Reserve` single-producer assumption documented (c2f975e).

Reconnect (REVIEW-RECONNECT.md): **M5–M7, L3 FIXED — Phase F, merge fd0a077.**
- [x] **M5** — deferred `mountWhenReady` ghost window fixed (per-origin snapshot
  epoch + per-record cancelled flag drop a superseded deferred upsert) `193f864`.
- [x] **M6** — self-closed relay socket wedge fixed (a fatal desync detaches the
  origin via `onFatalClose` instead of redialing the dead socket) `2d772a2`.
- [x] **M7** — post-confirm close now escalates SIGTERM→SIGKILL after a grace
  window (`terminateWindowedApp`) `49608f6`.
- [x] **L3** — teardown no longer gated on a grandchild holding the pipe
  (`cmd.WaitDelay`) `5285788`.
- [x] **L2** — auth-loss `lost-input` `bea9372`; head-steal banner DONE
  `9abf7e8` (new `shell.superseded` BE→FE message + FE "opened elsewhere"
  banner). Merge 968903d.
- [ ] **B1 deferred half** — move `handleAssetRead` streaming off the shell
  dispatch loop (`TODO(review F5)` in `shell_session.go`); the read-side
  liveness stamp already prevents the false idle-reap.
- [ ] Smaller notes list (5523ef3 residual focus gaps; login first-spawn flock
  doesn't re-`List`; bundles re-shipped every reconnect; …).

Display / X11-Wayland (REVIEW-X11-WAYLAND.md). **G1/G3/G5 done — Phase G, merge 15414ef.**
- [x] **G1** (== DATAPATH F5 input-stall half) — asset streaming moved off the
  shell dispatch loop (`streamAssetChunks`) `8bb9635`.
- [x] **G3 / #13 selection half** — `handle_set_selection` read now poll()-bounded
  (quiet-period timeout), no more leaked reader thread/fd `261c147`.
- [x] **G5 / #5** — re-investigated; no robust untitled-menu-vs-dialog signal
  exists (GTK app_id / decoration mode / min-max / commit timing / content
  probing all rejected). Left as a known limitation, recorded in-code `ec89a8d`.
- [x] **G2 / #13 keymap half** — DONE `f2ad961`: FE detects host layout
  (getLayoutMap) → new `display.set_keymap` app_msg → compositor recompiles the
  xkb keymap. Conservative (fr/de/us); full per-key remap is a future note.
  Manual verification pending (non-US typing).
- [x] **G4 / min-max size** — DONE `6e2c272`: xdg toplevel min/max → SessionWindow
  → FE clamps applyResize to [min,max]. Manual verification pending (Qt hard-min
  resize feel).
- Protocol-coverage gaps (Phase H, net-new). Merge f29323f:
  - [x] **H3 wp_viewporter** — `wlr_viewporter_create` `c694e1b`.
  - [x] **H2 primary selection** — manager + seat `request_set_primary_selection`
    (middle-click paste, X11 via xwm) `c694e1b`.
  - [x] **H7 request_minimize** — guest CSD minimize → `window.state` → wash WM
    `5ebf47d`. (set_app_id/icons + parent-stacking half still open.)
  - [ ] **H6 Xwayland -auth** — INFEASIBLE as described: build links SYSTEM
    wlroots (pkg-config), whose Xwayland argv has no `-auth` and binds an
    abstract socket (netns-global). Real fix = network-namespace isolation at
    the privileged spawn layer (D4-adjacent), NOT a compositor flag. Needs a
    product decision before starting.
  - [ ] **H1 wl_data_device drag-and-drop** — large (seat `request_start_drag`
    + a new FE drag surface + wire); highest UX blast radius.
  - [ ] **H5 xdg-activation** — `wlr_xdg_activation_v1_create` + a new
    focus-request message (compositor→router→FE).
  - [ ] **H4 presentation-time** — low value (wash captures surfaces, not the
    scene; Chromium/Firefox already fall back fine); risky to half-advertise.
  - [ ] **H7 set_app_id/icons + parent stacking** — cosmetic; new report
    messages + FE icon/stacking work.
  - [ ] Nested serial-less submenu chaining (`TODO` in `toplevel_setup_popover`).

Out of scope entirely: VP9/WebRTC transport work.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

- Hand-rolled insertion sorts (`cmd/wash/main.go`, `runtime_stats.go`) —
  intentional, avoids importing `sort` for two lines.
- `fm-replace.spec.ts` symlink test asserts `existsSync` only — by design.
- CORE_AUDIT 2.3 divergence traps (sparkline, Overlay screen-scope, token
  subset) — deliberate, see docs/CORE_AUDIT.md §2.
- `wash new-app` scaffold — deferred until a new app actually needs it.
