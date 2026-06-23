# Session prompt — CORE_AUDIT 2.2: single app registry

Paste this as the opening prompt for a dedicated session. It is self-contained.

---

You are picking up **item 2.2 of `docs/CORE_AUDIT.md`** ("single app registry feeding
the build") in the wash repo. The rest of Phase 2 (2.1 version, 2.3 format/createAppBus/
vite-factory, 2.4 fs structs) is already done on branch `wash-friction` — see that
branch's git log and the progress block at the top of `docs/CORE_AUDIT.md`. 2.2 was
split off because it is the **highest-blast-radius change in the audit**: it rewires the
build, and a mistake breaks every binary and every package. Treat it as its own gated
effort.

## Goal

Adding one windowed app today still touches ~6 Makefile sites by hand. Drive the
Makefile's per-app machinery from a single `APPS :=` list via `$(foreach)`/`$(eval)`,
so a new app is one line. **No behavior change** — the same binaries, same embedded
assets, same packages must come out.

## START BY RE-VERIFYING CURRENT STATE (the audit is from 2026-06-10 and is partly stale)

Two of the audit's five touchpoints are **already consolidated — do NOT redo them**:

- **Packaging (audit's "packaging ×3")** is already single-source: deb/rpm/apk install
  by looping over `packaging/wash.binaries` (`debian/rules`, `alpine/APKBUILD`). That
  file is generated from `BINS` via `make gen-pkg-binaries` and guarded in CI by
  `make check-pkg-binaries` (Makefile ~line 55-68). So once `BINS` is correct, packaging
  follows. ✅
- **e2e fixture (audit's "e2e fixture ×3 sites")** is already an `APP_BINS` map +
  `type AppName = keyof typeof APP_BINS` + a staging loop in `e2e/fixtures/router.ts`
  (~line 36-49, 249). ✅ (Optional stretch: have `APP_BINS` derive from the same source
  as `BINS` so they can't drift — but it already mirrors it.)

Re-grep to confirm both still hold before planning.

## The remaining work, in priority order

### 1. Makefile templating (the real win)

Today, each FE-bearing app has SIX hand-maintained Makefile sites (all in `Makefile`):
1. an entry in `BINS` (line ~36)
2. `<APP>_ASSETS` + `<APP>_STAMP` vars (block ~126-201)
3. a `web-<app>` phony target (~227-315) → `pnpm --filter @wash/app-<app> run build`
4. an `embed_dist` call wiring `apps/<app>/fe/dist` → `<APP>_ASSETS` in the stamp rule
   (~329-405)
5. a binary rule `$(OUT)/wash-<app>: $(<APP>_STAMP) | $(OUT)` (~439-516)
6. (multicall) membership in `MULTICALL_STAMPS`

**Model to copy:** the Makefile already uses the `define … endef` + `$(foreach m,$(LIST),
$(eval $(call RULE,…)))` pattern for packaging leaves — see `define PKG_LEAF_RULE` and the
`$(foreach m,$(PKG_MAP),$(eval …))` line (~line 1009). Reuse that idiom for an
`define app_rule` over a single `APPS :=` list.

**App categories the template MUST handle** (verify each against the tree):
- **FE-bearing window/panel apps** (have `apps/<app>/be/assets`, a `web-<app>` target,
  an embed step, and a stamp-dependent binary rule):
  session, about, connect, imageview, test, term, fm, edit, vscode-workbench, settings,
  top, disks, journal, syslogs, services, packages, net, washamp, music, radio,
  vscode (panel), netd (panel).
  - **Panel apps** `vscode` and `netd` build `src/panel.tsx → panel.js` (the rest build
    `src/main.tsx → index.js`). The vite side is already handled by `washAppConfig({entry,
    fileName})` (see `web/lib/vite-app.mjs`), so the Makefile only differs in that these
    two still just run `web-<app>` + embed like the others — no Makefile-level special
    case needed beyond the app existing. Confirm.
  - **`test`** is gated: its multicall import requires the extra `wash_test_app` build tag
    and `TEST_APP=1`. Keep that gating intact.
- **FE-less Go binaries** (binary rule only, NO assets/web/embed; **must be `.PHONY` or
  `make` silently never rebuilds them** — see the `[[wash makefile phony goservice]]`
  memory, this already bit wash-vscode once):
  bulk, priv, launch, notify, audio, remote, fswatchd, fswatch, sudo.
- **Special, NOT apps — leave hand-written:** `wash-router` and `wash-login` embed the
  shell runtime (`ROUTER_ASSETS` / `LOGIN_SHELL_ASSETS`), not an app FE. `wash-display`
  is the C++ compositor (its own CMake path). `wash-sudo` is appended to BINS separately.

Decide the `APPS` data shape — likely a space-separated list plus a way to mark
category (FE vs FE-less) and panel-ness, e.g. two lists (`FE_APPS`, `GO_APPS`) or
`name:kind` tokens parsed with `$(word)`/`$(subst)`. Keep it readable.

### 2. Generate the multicall imports (audit step 2)

`cmd/wash/imports_<app>.go` is one tiny file per app (build-tagged
`//go:build multicall && !no_app_<app>`, `import _ ".../apps/<app>/be"`). Generate
`cmd/wash/imports_generated.go` from the same `APPS` list via a `go:generate` or make
rule, deleting the per-app files. **Watch the tag nuances:** `test` needs
`&& wash_test_app`; a couple (audio, netd) carry explanatory comments; `vscode-workbench`
maps to file `imports_vscode_workbench.go` / package `apps/vscode-workbench/be`. Don't
lose the per-app opt-out tag (`!no_app_<app>`) — downstreams may rely on it.

### 3. Icon-sprite completeness check (audit step 5)

Add a build-time check that every app manifest's `Icon` exists in the sprite
(`web/shell/build-icons.mjs` `ICONS` array). A missing icon currently fails silently at
runtime. Wire it into the build or a `make check-icons`.

### 4. (Later, optional) `wash new-app` scaffold

Once 1-3 land, a `cp -r` template + appending one line to `APPS`. Out of scope unless
time permits.

## Verification gates (ALL required before merge — this is why it's its own session)

1. `make TEST_APP=1 all` green, and **diff the produced binary set against a pre-change
   build** — `ls out/` must be identical (same names), and a couple of bundles
   byte-comparable, to prove "no behavior change."
2. `make check-pkg-binaries` clean (BINS ↔ wash.binaries in sync).
3. Full e2e suite green (`cd e2e && pnpm install --ignore-workspace --frozen-lockfile`
   then run; note `e2e/` is a standalone pnpm project, not in the workspace).
4. `make all-package` — the 17-leaf Docker package matrix (deb/rpm/apk × arches) must
   still produce installable packages. This is the gate the audit was most worried about.
5. `make multicall TEST_APP=1` builds and the symlink set is unchanged.

Commit per logical step (tiered green gate: commit on build+unit green, the package
matrix before claiming done). Work on a fresh worktree off `main` (or rebased on
`wash-friction` once it merges), per the worktree workflow.

## Pointers
- `docs/CORE_AUDIT.md` §2.2 (original plan) and its top progress block (what's done).
- Branch `wash-friction` = the rest of Phase 2 (reference for style + the vite factory
  `web/lib/vite-app.mjs` which already abstracts the panel/cssCodeSplit variance).
- Memories: `[[wash makefile phony goservice]]`, `[[wash make rationalization]]`,
  `[[wash pkg single-source]]`, `[[wash multicall default]]`.
