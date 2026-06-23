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

> **Progress (2026-06-22 audit sweep):**
> - **1.5 — DONE** (landed via `[[wash auth harden]]`, not this branch).
>   `internal/login/ratelimit.go` (`rateLimiter`/`newRateLimiter`) is wired
>   as `authLimit` and enforced in `handleAuth` (`internal/login/server.go`).
> - **1.1 — PARTIAL.** The auth-harden merge added a router-**token** gate on
>   `/ws` (`tokenOK`: `wash_router` cookie / `?token=`), which blocks anonymous
>   LAN access. The doc's literal ask — verify wash-login's **HMAC session
>   cookie** on the raw router when not fronted by login — is still unimplemented.
> - **1.2 — DONE** (this branch). `internal/httpsec.HostAllowed` — a Host-header
>   allowlist, **permissive by default** (empty list ⇒ every Host accepted, so
>   LAN/mDNS deploys are unaffected; loopback names + bind host always pass).
>   Enforced in `router` `ServeHTTP` (`cfg.HostAllowlist`) and `login` `harden`
>   (`cfg.HostAllowlist`).
> - **1.3 — DONE** (this branch). `internal/router.requireLoopbackTCP` rejects an
>   ingress tcp backend addr that isn't `localhost`/loopback-IP; wired into
>   `ingress.publish`. (All in-repo publishers use unix sockets, so no behavior
>   change in practice — it closes the misuse path.)
> - **1.4 — DONE** (headers); CSP deferred. `internal/httpsec.SetSecurityHeaders`
>   sets `X-Frame-Options: SAMEORIGIN` + `X-Content-Type-Options: nosniff` +
>   `Referrer-Policy: same-origin` on router- and login-served responses, skipping
>   the `/app/<token>/` ingress proxy. A blocking CSP is **not** set yet: the shell
>   needs xterm/CodeMirror/Webamp inline/worker allowances, so it must be verified
>   in-browser before landing.
>
> **Still genuinely TODO:** 1.1 (verify wash-login's HMAC session cookie on the
> raw router — the token gate already blocks anonymous LAN access, so this is the
> defense-in-depth remainder) and the deferred CSP under 1.4.

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
- **Permissive by default**: with no allowlist configured the check
  is a no-op (logs the seen Host but accepts), so existing LAN/mDNS
  deployments don't break. Enforcement is opt-in via config; only
  then do off-list Hosts get rejected.

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
> - **2.3 vite factory — DONE.** 22 `apps/*/fe/vite.config.ts` collapse to
>   `washAppConfig()` in `@wash/ui/vite-app`; per-app overrides only.
> - **2.4 — PARTIAL.** Shared fm/edit request structs + `looksBinary` moved to
>   `internal/fs`; net error-reply standardization and `bus.Emit`-swallow
>   annotations deferred (see commit msg for why).
> - **2.2 app registry — DONE.** Three roster lists (`FE_APPS` /
>   `FE_PANEL_APPS` / `SVC_APPS`) + a `$(foreach)/$(eval)` pass drive every
>   per-app Makefile rule (web build, embed stamp, binary, vendor-sync,
>   MULTICALL_STAMPS) and `BINS`; `make gen-imports` generates the
>   `cmd/wash/imports_*.go` blank-imports; `make check-icons` (a multicall Go
>   test) gates manifest-icon ↔ sprite. Adding an app is one roster line +
>   `make gen-imports`. The audit's "packaging ×3" and "e2e fixture ×3" were
>   already single-source (wash.binaries loop / `APP_BINS` map) so untouched.
>   Verified no-behavior-change: identical out/ set, byte-identical binaries +
>   embedded bundles, identical multicall registration + symlink sets; fixed 3
>   latent `no_app_*` opt-out tag bugs + a stale RootVariant icon doc surfaced
>   by the icon check. Gates: unit + e2e + the 17-leaf package matrix.
>
> **Examined and deliberately NOT done (divergence traps, like fmtBytes):**
> - **2.3 sparkline** — top `Sparkline` uses pre-normalized [0..1] data, disks/
>   MirrorSparkline scale by a windowed max; dims + box differ. ~5 shared lines.
> - **2.3 Overlay adoption** — session's PrivUnlock/BulkConflict overlays are
>   `position: fixed` (screen-scoped, z-index 12000); `@wash/ui.Overlay` is
>   `absolute` (window-scoped). Adopting would shrink a security modal to the
>   app window. Would need a screen-scope mode on Overlay + careful e2e.
> - **2.3 token leaks** — the sidebar/disks/top colors overlap the chart/meter
>   palette that `[[wash UX tokens]]` keeps local on purpose; only the genuine
>   chrome subset should tokenize, and that needs screenshot verification.
>
> **Still genuinely TODO:** nothing in Phase 2 — the deferred 2.3 divergence
> traps above are deliberate no-ops, and 2.4's remainder is annotation-only.
> (2.2's optional stretch — a `wash new-app` scaffold — is left for when a new
> app is actually being added.)

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

Plan, incremental (✅ = landed on wash-friction):
1. ✅ Makefile templating: three roster lists (`FE_APPS` / `FE_PANEL_APPS` /
   `SVC_APPS`) + `define web_embed_rule|fe_bin_rule|panel_bin_rule|svc_bin_rule`
   and a `$(foreach …,$(eval …))` pass (modelled on `PKG_LEAF_RULE`). `BINS`,
   `vendor-sync`, and `MULTICALL_STAMPS` derive from the lists. The `.PHONY`
   FE-less / panel-service gotcha is encoded in the template choice, not
   per-app. (Note: the audit's "5 sites / `<APP>_ASSETS` vars" are gone — paths
   are computed inline as `apps/<app>/be/assets/.stamp`.)
2. ✅ `make gen-imports` writes the per-app `cmd/wash/imports_<app>.go`
   blank-imports from the roster (kept per-file, NOT one generated file, so the
   per-app `!no_app_<app>` opt-out survives); `make check-imports` (run by
   unit-test) gates drift. Normalized the opt-out tags `no_app_<name - → _>`,
   fixing music/radio (untagged) + washamp/vscode-workbench (wrong/colliding).
3. — Already single-source before this pass: deb/rpm/apk install by looping
   `packaging/wash.binaries` (generated from `BINS`, guarded by
   `check-pkg-binaries`). Nothing to do.
4. — Already done: `e2e/fixtures/router.ts` uses an `APP_BINS` map +
   `type AppName = keyof typeof APP_BINS` + a staging loop.
5. ✅ `make check-icons` — a multicall Go test (`cmd/wash/icons_test.go`)
   parsing `build-icons.mjs` and asserting every registered manifest icon is in
   the sprite; covered by e2e-test's `go test -tags=multicall ./cmd/wash/...`.
6. (Later) `wash new-app` scaffold — mostly `cp -r` of a template; deferred to
   when a new app is actually being added.

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
