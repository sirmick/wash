# wash — make-rationalization plan

Status: **design locked for build/test/package; run/clean/browser-vm + 3 topics still open.**
Goal: one goal-oriented `make` interface; the existing scripts become the engines it
delegates to (they don't disappear — they stop being what you type).

## Principle
- **`make` = the goal-verb layer.** Each target builds its own prerequisites and delegates
  the heavy lifting to the scripts:
  `make wash`→`build.sh` · `make unit-test`→`test.sh --no-e2e` · `make all-clean`→`clean.sh --all`
  · `make <arch>-<plat>-<pkg>-package`→`run_matrix.sh` (single-row filter).
- **Defaults live in the binaries/scripts, NOT in the make facade.** Make never passes
  `PORT=`/`--listen` overrides; it just invokes the script, which already defaults correctly.
- Scripts stay as the capability-aware front doors (build.sh already does auto-wlroots +
  "what this host could build but didn't" summary).

## Naming conventions
- Build product → named for the artifact: `make wash`, `make wash-multicall`
- Everything else → **`<qualifier>-<noun>`** (suffix-uniform):
  `*-test`, `*-package`, `*-image-vm`, `*-run-vm`, `*-clean`
- Bare verbs only for the primary launch/build actions: `wash`, `dev`, `run`

## Port scheme  (DONE — defaults set in code, 2026-06-13)
| service | port | default lives in |
|---|---|---|
| wash-login | **10000** | internal/runner/login/runner.go `defaultListen` |
| wash-router | **11000** | internal/runner/router/router.go `defaultListen` + run.sh |
| browser VM (in-tab riscv) | **12000** | wash-vm/run-browser.sh `PORT` + build.sh `--browser-vm` |
| qemu VM (wemu surface) | **13000** | wash-vm/run-qemu.sh `ADDR` |
(10000-13000 are forwarded through the user's firewall; non-overlapping so login+router+both
VM surfaces can run simultaneously. TODO: fix docs MULTIUSER.md/INTERNALS.md login :11000→:10000;
fix run-browser/run-qemu "run one or the other" comments — they're independent ports now.)

## The full make surface
([✓] agreed · [?] proposed/pending)

### BUILD
```
[✓] make wash               standalone, auto-includes wash-display if deps present (WASH_DISPLAY=0/1 forces)
[✓] make wash-multicall     busybox layout (out/multicall/)
```
### TEST  (standalone by default; each builds its prereqs)
```
[✓] make unit-test          go vet + go test ./... + FE-unit + component
[✓] make e2e-test           Playwright
[✓] make net-test           net-matrix + vm-net-test + net-vm e2e (builds openwrt+distro images)
[✓] make disks-test         vm-disks-test (builds alpine image)
[✓] make all-test           every test, BOTH layouts; does NOT package
```
### PACKAGE  `<arch>-<platform>-<pkg>-package`  (17 leaves + all)
```
[✓] make all-package        whole matrix (WASH_PKG_JOBS=N, WASH_PKG_DISPLAY=1)
[✓] wash:    {amd64,arm64,riscv64}×{ubuntu24,debian13,alpine321}  +  {amd64,arm64}×fedora40
[✓] display: {amd64,arm64}×{ubuntu24,debian13,fedora40}   (deb/rpm only; no alpine/riscv64)
    e.g. make arm64-ubuntu24-wash-package   make amd64-fedora40-display-package
    OpenWRT is NOT a package (runtime test row under net-test).
```
### VM — IMAGES  `<platform>-image-vm`
```
[?] make alpine-image-vm    host microvm (disks-test + qemu surface)
[?] make ubuntu-image-vm    \
[?] make debian-image-vm     | per-distro net-test images
[?] make fedora-image-vm    /
[?] make openwrt-image-vm   OpenWRT router (net-test / net-demo)
[?] make browser-image-vm   in-browser riscv (kernel+fw+rootfs+wasm) = today's make vm/rv
```
### VM — RUN  `<platform>-run-vm`  (each builds its image first)
```
[?] make browser-run-vm     serve in-browser riscv VM → :12000
[?] make qemu-run-vm        host qemu microvm + serve → :13000  (= today's run-vm/run-qemu.sh)
[?] make net-demo           3 OpenWRT microvms + consoles :8001-8003   (keep name? or openwrt-run-vm)
```
### RUN / DEV  (non-VM)
```
[?] make dev                router :11000 + Vite HMR :5173
[?] make run                run the built standalone router locally
```
### CLEAN  `<qualifier>-clean`
```
[?] make clean              in-tree build artifacts
[?] make tmp-clean          + /tmp runtime junk        (or fold into all-clean?)
[?] make docker-clean       + matrix docker images     (or fold?)
[?] make all-clean          everything (+ node_modules + go cache)
```
### STAYS INTERNAL (prereqs, not user verbs)
- 23 `web-*` FE targets (prereqs of `make wash`)
- fixtures: `fm-seed`, `seed-bulk-fixture`
- NO `install`/`deploy` verb — installation falls out of the native packages.

## OPEN — to discuss next
1. **Parallel build bits** — parallelize the host build (FE builds / Go binaries) like the
   matrix was parallelized? `make -j` already does Go; the FE Vite builds are the serial part.
2. **GitHub Actions** — point test.yml/matrix.yml at the new make verbs
   (`./test.sh --no-e2e`→`make unit-test`, etc.); confirm the matrix CI stays amd64-core + the
   `-no-auth` boot-smoke fix; decide if any new targets get CI coverage.
3. **stdout/stderr destination** — where spawned-process logs go (router→per-session log under
   run-root already done in spawn.go; standardize the rest? dev/run/vm logs to files vs stdout).
4. Micro-decisions: `qemu-run-vm` vs `wemu-run-vm`; `net-demo` keep vs `openwrt-run-vm`;
   clean tiers (4 targets vs clean+all-clean).

## Implementation order (once locked)
1. Add the make targets (generated leaves via `$(foreach)` over the matrix def).
2. Point CI workflows at the make verbs.
3. Regenerate COMMANDS.md to match.
4. Then close Thread A: commit, push to remote, get GH Actions green.

## Context (where this came from)
Session began: "pick up pkg-hermetic, reconcile with main, push." That's committed on local
main (unpushed): pkg-hermetic merge (502261e), `-no-auth` boot-smoke + parallel matrix
(e3e1255), capability-aware front door (270b1eb), multicall out-split (05dad96). The ORIGINAL
endpoint — **push to remote + green GH Actions — is still pending** (this make work precedes it
so the push is elegant + tested).
