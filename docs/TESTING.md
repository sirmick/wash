# Testing & coverage

How wash is tested, how to run each tier, and what coverage means here.

`make` is the interface — each test verb builds its own prereqs, then runs that tier.
The [README test matrix](../README.md#building--testing-each-part) is the
quick "how do I run X" reference; this doc is the why + the depth.

```sh
make unit-test           # go vet + go test + FE-unit + component + check-* guards
make test-race           # go unit suite under -race
make e2e-test            # Playwright, against the multicall layout
make all-test            # every tier, both layouts (see COMMANDS.md)
make push                # what CI runs, then git push only if green
```

---

## The tiers

wash is a deliberately **e2e-heavy pyramid**: most behaviour is proven
full-stack (router ↔ apps ↔ browser), with fast unit/component tiers
underneath as a regression net for the logic that's been factored into
pure kernels.

| Tier | Tool | What it proves | Run it directly |
|---|---|---|---|
| **Go unit** | `go test ./...` | backend logic — router, wire/CBOR, the `washnet` subsystem, priv, sdk, bulkops, login, pty | `go test ./apps/fm/...` |
| **FE pure/reactive** | `node --test` (`*.test.ts`), `--conditions=browser` for Solid reactivity | framework-free kernels + Solid reactive logic, no DOM | `node --test --conditions=browser <files>` |
| **FE component** | `vitest` + `vite-plugin-solid` + `jsdom` (`*.ctest.tsx`) | real component mount + DOM/events | `pnpm exec vitest run` |
| **e2e** | Playwright (Chromium) | the whole stack, on the host | `pnpm -C e2e exec playwright test <spec>` |
| **VM-backed e2e** | Playwright + a real Alpine microvm | the wash UI served *over the wire* from inside a booted VM (§ below) | `make net-test` |
| **In-browser VM** | chromium + the RISC-V WASM VM | the demo boots and the desktop mounts | `make browser-vm-test` |
| **Distro packaging** | Docker matrix | deb/rpm/apk/openwrt install + boot | `make all-package` / one leaf, `make openwrt-smoke` |

Roughly (counts drift — the test run prints the live tally): ~370 Go test
functions across 31 packages, ~175 FE unit cases (21 files), ~17 component
cases (5 files), ~270 e2e tests (49 specs).

### Why some Go packages have no `_test.go`

Of ~85 Go packages, ~31 carry unit tests. The rest are **not untested** —
they're covered a tier up:

- **App backends** (`apps/*/be`) are exercised end-to-end by the e2e suite,
  not by unit tests. This is the project's explicit strategy.
- **`cmd/*` entrypoints** (~34) are thin `os.Exit(run(...))` shims with no
  logic to unit-test.

Coverage (below) makes this concrete: app BEs read 0% at the unit layer but
60–90% once the e2e-exercised counters are merged in.

---

## make test verbs

| Verb | What it runs |
|---|---|
| `make unit-test` | build (test app) + FE-unit + component + `check-pkg-binaries` / `check-imports` / `check-versions` / `check-design` / `check-interactive` / `check-types` / `check-agent-protocol` + `go vet` + `go test` (excl. `wash-vm/vm`) |
| `make test-race` | the same Go packages under `-race` (in CI and `make push`) |
| `make e2e-test` | the full Playwright suite against the **multicall** layout (what ships) |
| `make standalone-smoke` | the per-app-binary layout (`out/singlecall/`): launch/spawn specs only |
| `make browser-vm-test` | boot the in-browser RISC-V VM in chromium; self-skips without `make browser-image-vm` |
| `make net-test` / `make disks-test` | the kvm VM gates (§ VM-backed e2e; `vm-disks-test`) |
| `make all-test` | unit + e2e + standalone-smoke + net-test + disks-test |
| `make test-all` | all-test + `all-package` (the whole pyramid) |
| `make coverage` | instrumented build → merged go-unit + e2e report (§ Coverage) |
| `make <arch>-<distro>-<pkg>-package` / `make all-package` / `make openwrt-smoke` | the Docker packaging matrix (leaf list in [COMMANDS.md](../COMMANDS.md)) |
| `make push` | unit-test + test-race + e2e-test + the four amd64 wash packages + openwrt-smoke, then `git push` (`ARGS=` for push args) |

Each verb exits non-zero on the first failing step. Pipe through `tee`, not
`tail` — `make … | tail` reports tail's exit status, not make's.

---

## Coverage

```sh
make coverage
# → coverage/coverage.txt   (open: go tool cover -html=coverage/coverage.txt)
```

This is **holistic Go coverage**: it builds coverage-instrumented binaries
(`go build -cover`, via the Makefile's `COVER=1`), runs the go-unit tests
*and* the full e2e suite with `GOCOVERDIR` set, then merges both pods with
`go tool covdata` into one module-wide number.

The headline (currently **~71% of statements**) is meaningful precisely
because it includes the e2e tier — app backends that have no `_test.go`
show real coverage (e.g. bulk/notify ~91%, net ~88%, fm ~64%, edit ~70%)
because the e2e suite drove them; `internal/router` ~73%, `sdk` ~69%,
`pty` ~89%.

### How e2e coverage is captured

Go's `-cover` only flushes counters on a *graceful* exit. The router stops
apps with `SIGTERM`, so an instrumented app killed by `SIGTERM` would write
nothing. On a coverage run (`GOCOVERDIR` set) `pkg/sdk/terminate.go` makes
sure a `SIGTERM`/`SIGINT` handler exists even when the app registered no
`sdk.OnTerminate` hook, and that handler exits via `os.Exit` (which runs the
runtime coverage hook).

### Known coverage gaps

- **`apps/netd/be/nm`** (~9%) and `networkd` (~63%): the e2e fixture pins
  `WASH_NETD_BACKEND=fake` for determinism, so the live NetworkManager /
  networkd D-Bus backends are deliberately bypassed. Run them on a host
  with NetworkManager to exercise the real path.
- **`internal/washvm/*`, `wash-vm/vm`**: only the VM-backed e2e drives these
  — covered when you run `make net-test` on a KVM host (their counters
  still need the same flush treatment as the SDK to be merged).
- **priv lock / reject / idle-wipe / `secureUnlock`**: e2e covers the
  approve→unlock→exec happy path; the reject/lock/idle/secure-erase paths
  are unit-test candidates (pure-ish `queue.go` state machine).
- **`pkg/sdk` coercion helpers**, **`internal/router` dev-reload**
  (only runs with `--dev`), **`pkg/wire` error branches**: expected
  low-value tail.

The FE tiers report ~100% of the *kernels under test* but a small fraction
of the whole 90k-LOC FE surface — the bulk of per-app UI is e2e-covered, by
design.

---

## VM-backed e2e (`make net-test`)

`net-vm-gate` / `net-vm-multi` boot a **real Alpine microvm** under
qemu/KVM and point Chromium at a proxy fronting it: the browser loads a
minimal host chrome, and the wash shell + app bundles stream **over the
wire from the in-guest router** (docs/NET.md §8). This proves the full
real stack — login, asset-over-wire, the net→netd cross-app relay, and a
real in-guest commit-confirm transaction with live auto-revert.

```sh
make net-test                       # build the images + run the net gates + net-vm e2e
make e2e-vm                         # just the two net-vm specs (builds their artifacts)
```

They need `/dev/kvm` + `qemu-system-x86_64` + `docker`, and three artifacts
(`make e2e-vm` builds them):
`out/vm/{vmlinuz,initramfs.gz}` (the image — `scripts/build-vm-image-alpine.sh`,
renders an Alpine+NetworkManager rootfs via Docker), `out/vm-chrome/`
(the host chrome), and `out/washvm-run` (the host VM runner/proxy). Without
those the specs **self-skip** with a one-line "run `make net-test`" hint —
they don't fail. A FE/shell change that affects the VM-served UI requires a
rebuild (`make e2e-vm` re-bakes the image), since the shell is served from inside
the VM.

> These specs are not in the default `make e2e-test` build (the artifacts +
> KVM aren't always present), so they're easy to forget. If you touch the
> net app, the apply/commit flow, or window layout, run `make net-test` —
> a regression there only shows up here.

---

## CI

`.github/workflows/ci.yml` runs on every push to `main`, every PR and every
`v*` tag, as two parallel jobs — **unit** (`make unit-test` + `make
test-race`) and **e2e** (`make e2e-test` + `make standalone-smoke`, with
Chromium + e2e deps installed) — then **package** (the amd64
ubuntu24/debian13/fedora40/alpine321 wash packages, arm64 debian13, and
`openwrt-smoke`), then on a tag **release** (publishes the packages; see
the README's Packaging section for the asset names). The kvm VM tiers
(`net-test`, `disks-test`) and `browser-vm-test` are not in CI; run them
locally. `make push` runs the CI set locally before pushing.

---

## Gotchas

- **Reactive Solid unit tests need `--conditions=browser`** — the default
  resolves Solid's non-reactive SSR build, so memos silently don't
  recompute. `make unit-test` sets it; a raw `node --test` won't.
- **Kernels must live in their own `*.ts` module**, not be imported from an
  app's `main.tsx` — the top-level `defineWashApp`/`window` use there breaks
  `node:test`.
- **Orphan e2e processes / inotify pressure**: an interrupted Playwright run
  can leak app/session children; >128 inotify instances breaks `fs.watch`
  silently. Before blaming a flake: `pgrep -fc 'wash-e2e-apps'` and reap
  with a `$$`-excluding loop (not `pkill -f`, which kills its own shell).
- **A fresh checkout** needs `pnpm install` *and*
  `pnpm --dir e2e install --ignore-workspace` (e2e/ is outside the
  workspace).
