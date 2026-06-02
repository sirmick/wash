# Testing & coverage

How wash is tested, how to run each tier, and what coverage means here.

`./test.sh` is the single entry point — it builds, then runs every tier.
The [README test matrix](../README.md#building--testing-each-part) is the
quick "how do I run X" reference; this doc is the why + the depth.

```sh
./test.sh                # build + all tiers (standalone layout)
./test.sh --help         # every flag
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
| **VM-backed e2e** | Playwright + a real Alpine microvm | the wash UI served *over the wire* from inside a booted VM (§ below) | `./test.sh --vm` |
| **Distro packaging** | Docker matrix | deb/rpm/apk/openwrt install + boot | `./packaging/run_matrix.sh` |

Roughly (counts drift — `./test.sh` prints the live tally): ~370 Go test
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

## `./test.sh` — modes & flags

| Flag | Effect |
|---|---|
| *(none)* | build + FE-unit + component + go-unit + e2e, `--standalone` layout |
| `--multicall` / `--both` | exercise the single multi-call `wash` binary layout (and/or both) |
| `--no-unit` / `--no-e2e` / `--no-build` | skip a stage (e.g. `--no-build` reuses `out/`) |
| `--filter <pat>` / `--workers <N>` | passed through to Playwright |
| `--coverage` | instrument + merge go-unit & e2e coverage into one report (§ Coverage) |
| `--vm` | also run the VM-backed net e2e for real (§ VM-backed e2e) |
| `--distro` / `--only-distro` | the Docker packaging matrix |

It exits non-zero on the first failing tier; output streams to stdout
(`./test.sh 2>&1 | tee /tmp/test.log` to capture).

---

## Coverage

```sh
./test.sh --coverage
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
apps with `SIGTERM`, and the SDK installs **no** signal handler in normal
operation — so an instrumented app killed by `SIGTERM` would write nothing.
`internal/sdk/coverage.go` installs a `SIGTERM`/`SIGINT` handler **gated
entirely on `GOCOVERDIR` being set** that exits via `os.Exit` (which runs
the runtime coverage hook). Outside a coverage run `GOCOVERDIR` is unset,
no handler is installed, and shutdown behaviour is byte-for-byte unchanged.

### Known coverage gaps

- **`apps/netd/be/nm`** (~9%) and `networkd` (~63%): the e2e fixture pins
  `WASH_NETD_BACKEND=fake` for determinism, so the live NetworkManager /
  networkd D-Bus backends are deliberately bypassed. Run them on a host
  with NetworkManager to exercise the real path.
- **`internal/washvm/*`, `wash-vm/vm`**: only the VM-backed e2e drives these
  — covered when you run `./test.sh --vm` on a KVM host (their counters
  still need the same flush treatment as the SDK to be merged).
- **priv lock / reject / idle-wipe / `secureUnlock`**: e2e covers the
  approve→unlock→exec happy path; the reject/lock/idle/secure-erase paths
  are unit-test candidates (pure-ish `queue.go` state machine).
- **`internal/sdk` coercion helpers**, **`internal/router` dev-reload**
  (only runs with `--dev`), **`internal/wire` error branches**: expected
  low-value tail.

The FE tiers report ~100% of the *kernels under test* but a small fraction
of the whole 90k-LOC FE surface — the bulk of per-app UI is e2e-covered, by
design.

---

## VM-backed e2e (`--vm`)

`net-vm-gate` / `net-vm-multi` boot a **real Alpine microvm** under
qemu/KVM and point Chromium at a proxy fronting it: the browser loads a
minimal host chrome, and the wash shell + app bundles stream **over the
wire from the in-guest router** (docs/NET.md §8). This proves the full
real stack — login, asset-over-wire, the net→netd cross-app relay, and a
real in-guest commit-confirm transaction with live auto-revert.

```sh
./test.sh --vm                      # preflight + build artifacts + run them
./test.sh --vm --no-unit --filter net-vm   # just the VM specs
make e2e-vm                         # equivalent make entry point
```

`--vm` preflights `/dev/kvm` + `qemu-system-x86_64` + `docker` (aborts with
the missing list) and builds three artifacts the specs need:
`out/vm/{vmlinuz,initramfs.gz}` (the image — `scripts/build-vm-image-alpine.sh`,
renders an Alpine+NetworkManager rootfs via Docker), `out/vm-chrome/`
(the host chrome), and `out/washvm-run` (the host VM runner/proxy). Without
those the specs **self-skip** with a one-line "run `./test.sh --vm`" hint —
they don't fail. A FE/shell change that affects the VM-served UI requires a
rebuild (`--vm` re-bakes the image), since the shell is served from inside
the VM.

> These specs are not in the default `./test.sh` build (the artifacts +
> KVM aren't always present), so they're easy to forget. If you touch the
> net app, the apply/commit flow, or window layout, run `./test.sh --vm` —
> a regression there only shows up here.

---

## CI

`.github/workflows/test.yml` runs `./test.sh` on every push to `main` and
every PR, in two parallel jobs: **unit** (`--no-e2e`: build + FE unit + go
unit) and **e2e** (`--no-unit`: build + Playwright, with Chromium + e2e
deps installed). The VM-backed and distro tiers are not in CI (they need
KVM / Docker-in-Docker); run them locally.

---

## Gotchas

- **Reactive Solid unit tests need `--conditions=browser`** — the default
  resolves Solid's non-reactive SSR build, so memos silently don't
  recompute. `./test.sh` sets it; a raw `node --test` won't.
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
