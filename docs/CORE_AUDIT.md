# wash — core audit (next tranche of work)

> The standing core-audit / remediation plan (originated 2026-06-10). This
> is the live backlog: Phase 1 (security hardening) shipped; Phases 2–3
> (friction killers + structural splits) are the next tranche. Supersedes
> the one-off `AUDIT.md` snapshot (deleted; in git history).


Findings from a full-codebase audit (security, duplication/smell,
feature-addition friction) turned into an ordered work plan. Scope was
~347 Go files / ~195 TS files across 20 apps and 18 binaries,
excluding `branches/` and build output. High-severity security claims
were verified by hand against the code; refuted findings are recorded
at the bottom so they don't get re-reported next audit.

Complements `docs/AUDIT.md` (2025 snapshot, router/session-focused)
and `docs/TECH_DEBT.md`. Suggested branch split per the usual
worktree flow: `wash-hardening` (phase 1) and `wash-friction`
(phases 2–3).

---

## Phase 1 — security hardening (branch: wash-hardening)

### 1.1 Gate `/ws`, `/screenshot`, `/app/` on session auth  ← the one that matters

- `internal/router/http.go:47-53` registers all routes with no
  session check; `internal/router/ingress.go:29-32` documents the
  gap ("the shell /ws transport is itself still localhost-trust").
- With the deliberate `0.0.0.0` bind, anyone on the LAN gets a full
  desktop session — terminal included, i.e. arbitrary code execution
  as the user.
- The mechanism already exists: wash-login mints an HMAC cookie
  (`internal/login/cookie.go`, crypto/rand key, 0600) and hands off.
  When the router is NOT fronted by wash-login, verify that same
  cookie on `/ws`, `/screenshot`, and `/app/` upgrades/requests.
- The websocket same-origin guard (`InsecureSkipVerify: false`,
  coder/websocket's enforcing default) blocks malicious *websites*
  but not direct LAN connections, and DNS rebinding bypasses it
  (Origin and Host both carry the attacker's domain).

### 1.2 Host-header allowlist on router + login servers

- Closes the DNS-rebinding variant for both the websocket origin
  check (1.1) and SameSite-cookie CSRF in one place.
- Accept: configured bind host, `localhost`, `127.0.0.1`, `[::1]`,
  plus an opt-in list for LAN hostnames.

### 1.3 Ingress: enforce loopback-or-unix backends

- `internal/router/ingress.go:75-80` — `publish()` validates only
  `network ∈ {unix, tcp}` and non-empty addr. The doc comment says
  tcp is "dialed on loopback" but nothing enforces it: an app can
  publish `10.0.0.5:5432` and the router proxies it, token-keyed but
  otherwise unauthenticated, to anyone.
- Fix: reject tcp addrs that don't resolve to a loopback IP. Keep
  unix sockets as the preferred path. Log every publish (already
  done) and every rejection.

### 1.4 Security headers

- No CSP / X-Frame-Options anywhere (`internal/router/http.go`,
  `internal/login/server.go`).
- Add `X-Frame-Options: SAMEORIGIN` everywhere except the
  `/app/<token>/` iframe routes; add a starter CSP on the shell
  (`default-src 'self'`; allow what xterm/CodeMirror/Webamp need —
  expect some `style-src 'unsafe-inline'`).

### 1.5 Login rate limiting

- `internal/login/server.go:557` `handleAuth` has no throttling.
  PAM faildelay via `su` (~2s) limits a single connection but not
  parallel ones.
- Small per-IP token bucket in `handleAuth`; in-memory is fine.

---

## Phase 2 — friction killers (branch: wash-friction)

> **Progress (branch wash-friction):**
> - **2.1 — DONE.** `internal/version.Version`, single source via root `VERSION`,
>   stamped through `-ldflags -X`; 33 per-binary literals removed.
> - **2.3 format — DONE.** `@wash/ui/format` (`fmtBytes`/`fmtRate`/`fmtUptime`/
>   `fmtClockTime`), byte/rate display standardized on KB/MB/GB desktop-wide.
> - **2.3 createAppBus — DONE.** `@wash/ui` `createAppBus` centralizes the
>   wash:msg/wash:state listener+cleanup+send plumbing; migrated 13 apps
>   (about/connect/disks/journal/syslogs/packages/imageview/top/music/radio/
>   washamp/vscode-workbench/services). fm/edit (createBus correlation),
>   term/session (multi-listener), wash-test (raw element) keep bespoke setups.
>   Verified via per-app e2e.
> - **2.4 — PARTIAL.** Shared fm/edit request structs + `looksBinary` moved to
>   `internal/fs`; net error-reply standardization and `bus.Emit`-swallow
>   annotations deferred (see commit msg for why).
> - **2.3 rest (Overlay adoption, token leaks, vite factory) and 2.2 (app
>   registry) — TODO.** Sparkline left local (components diverged, like fmtBytes).

### 2.1 `internal/version` package  ← biggest win per effort

- The version string is hardcoded in ~80 places: every
  `apps/*/be/app.go`, `internal/runner/{router,login}/runner.go`,
  `cmd/wash-sudo/main.go`, packaging (`alpine/APKBUILD`,
  `rpm/wash.spec`), `e2e/package.json`, test fixtures.
- Create `internal/version/version.go` with `const Version`;
  apps import it; packaging stamps via `-ldflags -X`.
- Known gotcha (memory + prior bump): two runner files share the
  `0.0.0.0:11000` literal with the version — edit those by hand,
  not by sed.

### 2.2 Single app registry feeding the build

Adding one windowed app currently touches ~15 files across 6
registration systems:

| touchpoint | where |
|---|---|
| Makefile ×5 sites | `BINS`, `<APP>_ASSETS`/`_STAMP`, `web-<app>`, stamp rule, binary rule (+ `MULTICALL_STAMPS`) |
| multicall import | `cmd/wash/imports_<app>.go` |
| packaging ×3 | `debian/wash.install`, `alpine/APKBUILD`, `rpm/wash.spec` |
| e2e fixture ×3 sites | `e2e/fixtures/router.ts` (`<APP>_BIN`, type union, `if (wanted...)` block) |
| icon sprite | `web/shell/build-icons.mjs` `ICONS` array |

Plan, incremental:
1. Makefile `define app_rule` + `$(foreach ...)` templating over a
   single `APPS :=` list. Also structurally fixes the
   `.PHONY`-for-FE-less-services gotcha (bit wash-vscode once).
2. Generate `cmd/wash/imports_generated.go` from the same list
   (`go:generate` or a make rule).
3. Drive the three packaging lists from `APPS` in the build scripts.
4. Collapse `e2e/fixtures/router.ts` to a generated `BINS` map +
   `export type AppName = keyof typeof BINS` + a loop instead of the
   per-app `if` blocks.
5. Build-time check that every manifest icon exists in the sprite.
6. (Later) `wash new-app` scaffold — mostly `cp -r` of a template
   once 1–5 land.

### 2.3 Shared FE plumbing in `@wash/ui` / `@wash/fs-client`

- **`format` module**: `fmtBytes`/`fmtTime`/`fmtRate`/`fmtUptime`
  are reimplemented in about, disks, top, syslogs (fm already uses
  `fs-client.humanSize()`). One module, four app diffs. Cheapest win
  in the audit.
- **`createAppBus()`**: 14+ apps repeat the
  `addEventListener('wash:msg')` / `onCleanup` dance; only fm/edit
  get request/reply correlation via fs-client `createBus()`. One
  shared wrapper (host listener + cleanup + reqid correlation)
  eliminates a class of leak/lost-message bugs.
- **Overlay adoption**: `apps/session/fe/src/sidebar/PrivUnlockOverlay.tsx`
  and `BulkConflictOverlay.tsx` hand-roll backdrop/z-index/animation
  that `@wash/ui.Overlay` already provides.
- **Sparkline component**: top (`Sparkline`, `MirrorSparkline`) and
  disks (`Spark`) duplicate ring-buffer + SVG path logic →
  `@wash/ui/sparkline`.
- **Token leaks**: 30+ hardcoded colors in the session sidebar,
  disks' danger color, top's progress-bar RGBs — route through
  `@wash/ui` tokens per the house rule; add missing token variants
  (muted fg, section bg, warning shade) rather than inventing
  hex values per app.
- **Shared vite config factory**: 14 near-identical
  `apps/*/fe/vite.config.ts` → one base factory, per-app overrides
  only (term keeps its `cssCodeSplit: false`).

### 2.4 Go shared-code cleanups (small, mechanical)

- Move fm/edit request structs (`listReq`, `readReq`, `writeReq`,
  `renameReq`, `pathReq`, …) into `internal/fs/wire.go` next to the
  already-shared response types
  (`apps/fm/be/app.go:147-180` ≈ `apps/edit/be/app.go:140-169`).
- Dedupe `looksBinary()` (`apps/fm/be/app.go:595`,
  `apps/edit/be/app.go:393`) into `internal/fs`.
- Standardize error replies on fm's `sdk.Err{Code: wfs.ErrCode(err)}`
  idiom; migrate net's manual `errors.As` mapping
  (`apps/net/be/app.go:178`).
- `_ = bus.Emit(...)` swallows: either an `EmitLogged` variant or a
  one-line "safe to drop because X" comment at each site.

---

## Phase 3 — structural splits (opportunistic, one app per touch)

No behavior change; do these when already in the file, not as a
dedicated sprint.

- `apps/edit/fe/src/main.tsx` (2968 lines) → tabs / sidebar / status.
- `apps/fm/fe/src/main.tsx` (2456) → tree / preview / toolbar
  (note: FE refactor phase-4 kernel extraction already covers some
  of this — see `docs/FE_REFACTOR_PLAN.md`).
- `apps/session/fe/src/main.tsx` (1777), `apps/top/fe/src/main.tsx`
  (1443), `apps/net/fe/src/main.tsx` (1053).
- `apps/priv/be/queue.go` (1174) → state machine vs exec modes.
- `apps/netd/be/app.go` (954; single 212-line `registerHandlers`)
  → net / wifi-read / wifi-mutate handler files.
- `internal/bulkops/bulkops.go` (911) → worker / conflict files.
- `internal/wire/msgs_event.go` (865) → split by concern, wire
  format unchanged.

---

## Audited and fine (don't re-flag)

- **bulkops symlinks**: `copyTree` uses `Lstat` and recreates
  symlinks via `Readlink`+`Symlink` — never follows them. The
  "symlink traversal" finding was false.
- **priv password hygiene**: mlock'd buffer, `PR_SET_DUMPABLE` off,
  `RLIMIT_CORE=0`, explicit wipe (`apps/priv/be/securemem.go`,
  `hardening.go`). Better than the textbook fix.
- **Cookie machinery**: crypto/rand key, 0600 file, constant-time
  HMAC compare. The "expiry timing leak" is theoretical and reveals
  nothing useful.
- **Path confinement**: `internal/fs.Confine()` is correct.
- **9-line `apps/*/be/cmd/main.go` shims and per-app ready-log
  lines**: acceptable boilerplate, leave alone.
- **washvm-\* binaries**: guest-side, built by the VM image scripts,
  correctly absent from the host `BINS`.
