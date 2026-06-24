# wash — TODO / backlog

One consolidated, actionable list. Detailed design/rationale lives in the
`docs/` audit docs (linked per section); this file is the short "what's
left" view. Items are grouped by area, roughly high-value first.

Last consolidated: 2026-06-22 (after the CORE_AUDIT Phase 1 + TECH_DEBT
P2/P3 merges).

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
- [ ] **SDK `pendingCall[T]` generic.** Three near-identical pending
  registries (`pendingOpens`, `pendingClipboardGet`, `requestIDs`/`pending`
  in bus.go) — collapse into one generic correlation helper. Touches
  concurrency; add a focused test.
- [ ] **`bus.go classifyKind` hardcoded suffix matching.** Replace the
  `strings.HasSuffix` ladder with a declared mapping.
- [ ] **`internal/router/control.go controlReq` catch-all union.** One
  struct serves two protocols (launch/msg + priv.run); split.
- [x] **Collapse the two ringbuf impls.** Unified into a single
  `ringBuffer` (`internal/router/ringbuf.go`): added an internal mutex
  (needed by spawn's two concurrent pipe writers), made `Write` satisfy
  `io.Writer`, added `String()`; migrated the spawn log-tail buffer and
  deleted the duplicate `ringBuf`. All tests preserved.
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
- [ ] **Stringly-typed FE message handlers.** `apps/top/fe/src/main.tsx`
  `handleBE = (m: any)` and `web/shell/src/main.tsx` `switch (msg.t)` with
  `as` casts — give them discriminated-union types.
- [ ] **(optional) Phase 7** — same playbook for `session` / `top`.

## window manager / shell

- [x] **"Open X" buttons now raise an already-open app.** Buttons like the
  Settings box's "Open network settings" launch the target app and bring it
  to the foreground when it isn't running — but if the app was *already* open,
  clicking did nothing visible (the existing window stayed in the z-order
  instead of being raised/focused; instancing=single apps even stacked a
  duplicate). Fixed at the launch seam: `Router.launchOrRaise` raises the
  existing window for single/singleton apps across all three launch paths
  (app spawn-request, launcher `shell.launch`, control-socket launch).

## system monitor (top)

- [ ] **`% mem` and RSS look wrong.** The memory percentage and RSS columns
  in the system monitor display funny/incorrect values — investigate the
  per-process mem accounting (`apps/top/be`) and the FE formatting.

## wash-fm

- [ ] **Expand-folder scroll anchoring.** Expanding a folder inserts rows
  above the viewport so content shifts down; scroll should compensate for
  the added length so the clicked row "stays where it is" visually.

## terminal

- [ ] **Double-click word selection.** Select just a word, breaking on any
  special char/symbol (not only whitespace).
- [ ] **Copy keybinding on selection.** On PC, `Ctrl-C` = copy when a
  selection exists, else pass through as SIGINT (macOS uses `Cmd-C`, no
  conflict).
- [ ] **Menubar with the basics** — themes, tab color, Edit menu
  (copy/paste/etc.).

## Packaging / deploy  (docs/MULTIUSER.md, deb/rpm/apk)

- [ ] **Move `setcap` into the deb postinst** (env-agnostic; keep the
  systemd `ExecStartPre` as belt-and-suspenders) so `cap_setuid,cap_setgid,
  cap_kill` are set at install time under systemd / OpenRC / supervisord /
  bare container alike. Document the bounding-set caveat.
- [ ] **Ship a supervisord drop-in** for no-systemd/container hosts — as an
  example (`/usr/share/wash-login/supervisord.conf` + a doc line), or a
  small `wash-login-supervisor` subpackage that `Depends: supervisor`. Do
  NOT install an active conf from the main package.
  - Raised from the homezone estate (wash under MikroTik RouterOS OCI +
    supervisord). Acceptance: a Debian+supervisor container serves login
    on :10000 with working uid-switching, no manual setcap, no hardcoded
    `--allow-insecure-cookie` downstream.

## Test suite hygiene  (docs/TECH_DEBT.md P3)

- [ ] **e2e fixed-sleep flakiness.** `viewport.spec.ts` uses
  `waitForTimeout(280/260)` + hardcoded `rgb(...)` color asserts — replace
  with state-based waits.
- [ ] **8-worker suite timing-race flakes** (fm-be / net-vm / music /
  settings) papered over by `retries:1` + a 12s control-socket timeout —
  root-cause properly. (See memory: e2e load flakes.)
- [ ] **Sidebar e2e order.** 3 fm-related specs pass alone but fail in the
  full suite after M7; not root-caused.

## Won't-do / deliberate no-ops (recorded so they don't get re-flagged)

- Hand-rolled insertion sorts (`cmd/wash/main.go`, `runtime_stats.go`) —
  intentional, avoids importing `sort` for two lines.
- `fm-replace.spec.ts` symlink test asserts `existsSync` only — by design.
- CORE_AUDIT 2.3 divergence traps (sparkline, Overlay screen-scope, token
  subset) — deliberate, see docs/CORE_AUDIT.md §2.
- `wash new-app` scaffold — deferred until a new app actually needs it.
