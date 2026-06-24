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
- [x] **SDK `pendingCall[T]` generic.** Collapsed *six* near-identical
  correlation registries (channel opens, clipboard get, ingress publish,
  app restart, window create, and the `bus.Call` reply map) into one
  generic `pendingCalls[K comparable, T any]` (`internal/sdk/pending.go`)
  with register/resolve/cancel. Focused concurrency test + `-race`; loopback
  integration green.
- [x] **`bus.go classifyKind` hardcoded suffix matching.** Replaced the
  `strings.HasSuffix` ladder with a declared `bulkKindSuffixes` slice
  (source of truth) that the function iterates; test locks the list.
- [x] **`internal/router/control.go controlReq` catch-all union.** Split
  into `controlHeader` (dispatch peek), `launchMsgReq`, and `privRunReq`
  via a two-phase decode; wire JSON unchanged. New dispatch routing test.
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
- [x] **Stringly-typed FE message handlers.** `top` `handleBE` now takes a
  `TopBEMsg` union; `web/shell` `onCtrl` takes a 17-variant `ShellCtrlMsg`
  union — 12 `as` casts + 2 `any` params removed. `@wash/ui` `createAppBus`
  made generic over the message type (default `AppBusMessage`, so existing
  consumers are unchanged) to let an app pass its own narrowed union.
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

- [x] **`% mem` and RSS look wrong.** Root cause: an off-by-one in
  `apps/top/be` `parseStat` — every `/proc/<pid>/stat` field after `stime`
  was read one slot too far, so RSS came back as `rsslim` (field 25, usually
  `RLIM_INFINITY`) and `%mem` = RSS/total inherited the garbage. Fixed the
  indices (rss→`rest[21]`, vsize→`rest[20]`, nice→`rest[16]`,
  num_threads→`rest[17]`, starttime→`rest[19]`); the same drift had also
  corrupted `virt`/nice/threads/start-time. Pinned by `proc_test.go`. FE
  formatting was fine.

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

## terminal

- [x] **Double-click word selection.** Set xterm's `wordSeparator` to the
  full ASCII-punctuation set (minus `_`), so double-click selects one
  identifier/path segment instead of stopping only at whitespace.
- [x] **Copy keybinding on selection.** Plain `Ctrl-C` copies the selection
  (then clears it so a second press interrupts) and otherwise falls through
  as SIGINT; on macOS `Cmd-C` copies and `Ctrl-C` stays SIGINT. e2e:
  `term-select-copy.spec.ts`.
- [ ] **Menubar with the basics** — themes, tab color, Edit menu
  (copy/paste/etc.).

## Packaging / deploy  (docs/MULTIUSER.md, deb/rpm/apk)

- [ ] **Move `setcap` into the deb postinst** (env-agnostic; keep the
  systemd `ExecStartPre` as belt-and-suspenders) so `cap_setuid,cap_setgid,
  cap_kill` are set at install time under systemd / OpenRC / supervisord /
  bare container alike. Document the bounding-set caveat.
- [x] **Ship a supervisord drop-in** for no-systemd/container hosts. Example
  program (docs only, not activated) installed at
  `/usr/share/wash-login/supervisord.conf` by all three packagers
  (deb/rpm/apk); runs wash-login as root (root holds the caps → no setcap)
  and sources args from the single-source `/etc/default/wash-login`, with a
  documented hardened `user=wash-system` alternative. Doc line in
  docs/MULTIUSER.md "Out-of-the-box setup". Config validated with a
  configparser parse; full Debian+supervisor container acceptance still to
  run via the package pipeline.

## Test suite hygiene  (docs/TECH_DEBT.md P3)

- [x] **e2e fixed-sleep flakiness.** `viewport.spec.ts` no longer uses
  `waitForTimeout` or `rgb(...)` color asserts: the pager cell now exposes a
  stable `data-active` attribute (flipped synchronously by `setViewport`,
  independent of the cam CSS transition) and the spec waits on/asserts that
  via auto-retrying `toHaveAttribute`. Verified 4× repeat-each, green.
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
