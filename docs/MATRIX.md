# wash — distro matrix & native packaging plan

Plan for shipping wash as a native `.deb` / `.rpm` / `.apk`, and for
proving the per-distro pieces (init system, package manager, log
sources) work on every distro we claim to support.

Lifted from the ferrite packaging pattern
(github.com/sirmick/ferrite/tree/main/packaging): two-stage Dockerfile
per package format, host-staged source tarball, `run_matrix.sh` driver
with per-row OK/FAIL summary.

## Goal

Make "wash works on alpine, debian, fedora, ubuntu" a testable claim
instead of "works on whichever box the developer is on." Three things
in scope:

1. **Logging** — wash-syslogs against the distro-canonical log files;
   wash-journal against real journald where present.
2. **Services** — real systemd on debian/fedora/ubuntu, real openrc on
   alpine. Today's `services.spec.ts` only stubs systemctl.
3. **Package management** — real apt/dnf/apk through the wash-packages
   backend. Today only apt has a backend at all.

Plus a native package per distro so the install path itself is tested,
not just `cp out/* /usr/bin/`.

## Architectural commitment

- **The matrix runs unit tests only**, not e2e. The FE and wire are
  distro-agnostic Solid+web-components code; re-running 30+ chromium
  specs per distro buys nothing. Distro-specific risk lives in
  per-backend Go code and the install path; both are testable without
  a browser.
- **The host e2e suite stays as it is.** Stubbed systemctl, stubbed
  PATH, fast dev loop. Not touched by this plan.
- **Native packaging, not nfpm.** Per-distro tooling (`debian/rules`,
  `rpm/wash.spec`, `alpine/APKBUILD`) is the source of truth — same
  shape ferrite uses. nfpm's single-yaml convenience isn't worth
  losing per-format scriptlet idioms (systemd unit registration,
  openrc runlevel links, user/group creation).
- **Sanitized build environment — compile in Docker, never on the
  host.** A `make-source-tarball.sh` produces a SOURCE-only tarball;
  `packaging/Dockerfile.build` is a per-distro base image carrying the
  pinned toolchain (Go 1.25 + Node 22 + pnpm 11, plus the wlroots stack
  when `WITH_DISPLAY=1`); `packaging/Dockerfile.binaries` compiles
  `out/wash-*` inside that image for a given `GOARCH`. The packaging
  Dockerfiles then `dpkg-buildpackage` / `rpmbuild` / `abuild` over the
  resulting source+`out/` tree (their `rules`/`%build` stay no-ops —
  the binaries are genuinely prebuilt, just prebuilt hermetically). A
  shipped package therefore never inherits whatever Go/Node/cmake the
  host happens to carry. Network IS allowed during the in-container
  build (`go mod download`, `pnpm install`); fully-offline vendoring is
  a later hardening step.
- **Pure-Go core is distro-independent and cross-compiles; wash-display
  is native.** The core is `CGO_ENABLED=0` static, so its binaries are
  byte-identical across distros — cross-compiled ONCE per arch on the
  amd64 host (`Dockerfile.binaries`, just a `GOARCH` env, no qemu) and
  shared by every row. wash-display is C++ linked against wlroots and
  can't sanely cross-compile, so it builds in a container OF the target
  arch via `docker buildx --platform` + qemu-user (`Dockerfile.display`),
  emulated for arm64/riscv64. The arch-independent Vite FE (panel.js) is
  built on the amd64 host (`$BUILDPLATFORM`) and copied in — which is
  also what makes riscv64 work, since Node ships no riscv64 build.
- **wash-display bundles its own wlroots (vendored 0.17.4).** It links
  the vendored `wash-display/third_party/wlroots` (built trimmed:
  headless + pixman + Xwayland; no gles/vulkan/drm/gbm), NOT the distro's
  libwlroots — so the compositor is pinned to the wlroots API
  `compositor.cpp` targets, independent of whatever the distro ships
  (debian 13 has 0.18, ubuntu/fedora 0.17, many have none). The shared
  lib is bundled in the package under `/usr/lib/wash` and found via an
  rpath; the package depends only on wlroots' base libs
  (wayland-server ≥ 1.22, libdrm, pixman, xkbcommon, xcb*, xwayland),
  which is the one hard floor — it rules out debian 12 (wayland 1.21),
  hence display targets ubuntu 24.04 / fedora 40 / debian 13.
- **Packaging + verify run native (emulated).** Each package is built
  with `docker buildx --platform`, so even arm64/riscv64 rows run the
  full install smoke + distro-integration + daemon-boot under qemu
  rather than skipping. (A real arm64/riscv64 host builds the same
  thing natively and fast — no qemu.)

## Workstreams

### WS-A — new package backends

Without dnf and apk backends, the fedora and alpine rows produce a
package that installs cleanly but renders "no supported package
manager" in wash-packages. Backends are a prerequisite for the matrix
having anything to assert against on two of the four distros.

- `apps/packages/be/dnf.go` — Search via `dnf search`, Install/Remove
  via `dnf -y install/remove`, UpgradePkg via `dnf -y upgrade <pkg>`.
  GlobalActions: refresh-metadata, upgrade, autoremove, clean.
- `apps/packages/be/apk.go` — Search via `apk search -v`, Install via
  `apk add`, Remove via `apk del`, UpgradePkg via `apk upgrade <pkg>`.
  GlobalActions: update, upgrade-available, cache-clean.
- `*_test.go` parser tests in the shape of `apt_test.go`.
- `Detect()` probes apt → dnf → apk. Only one is installed per
  distro, so order is for readability not precedence.

Plus distro-integration tests (see WS-C):

- `apps/packages/be/{apt,dnf,apk}_distro_test.go`
- `apps/services/be/{systemd,openrc}_distro_test.go`

All carry `//go:build distro_integration` so they skip on dev boxes
and run unconditionally inside the matrix containers.

### WS-B — native packages

New source trees at the repo root, matching the layout each packager
expects:

```
debian/
  control            # binary deps, conflicts, section
  changelog          # versioned; dch -i for bumps
  rules              # dh sequence; install goes through wash.install
  wash.install       # dh_install srcdest → dest mapping
  wash-login.service # systemd unit for the M2+ login front-door
  postinst           # creates wash-system user + wash group,
                     # sets caps on /usr/bin/wash-login,
                     # enables wash-login.service
  postrm             # disables + removes user/group on purge
  compat             # debhelper compat level

rpm/
  wash.spec          # %files, %install, %pre, %post, %preun,
                     # systemd_post / systemd_preun macros

alpine/
  APKBUILD           # source, deps, install hooks, package() fn
  wash-login.initd   # openrc init for wash-login
                     # (sibling to existing image/rootfs/wash-router.initd)
  wash-login.pre-install
  wash-login.post-install
```

### Install layout (cross-format)

Locked across all three formats. Matches `MULTIUSER.md` §314.

```
/usr/bin/
  wash-router        # per-user/per-session router
  wash-login         # privileged front door (M2+)
  wash-session
  wash-fm
  wash-term
  wash-about
  wash-priv
  wash-edit
  wash-bulk
  wash-settings
  wash-top
  wash-syslogs
  wash-services
  wash-packages
  wash-journal
  wash-sudo          # optional; WASH_NO_SUDO=1 → omitted from package

/etc/wash/
  secret.key         # generated lazily on first wash-login start.
                     # mode 0600, owner wash-system. NOT created in
                     # postinst — saves the package needing entropy
                     # at install time.

/usr/lib/systemd/system/wash-login.service  # systemd distros
/etc/init.d/wash-login                       # alpine (openrc)
```

postinst (deb / rpm %post / apk post-install):

- `getent group wash >/dev/null || groupadd --system wash`
- `getent passwd wash-system >/dev/null || useradd --system --gid wash --home-dir /var/lib/wash --shell /usr/sbin/nologin wash-system`
- `setcap cap_setuid,cap_setgid,cap_kill+ep /usr/bin/wash-login`
- `usermod -aG shadow wash-system` (so wash-login can read /etc/shadow for the default auth backend)
- systemd: `systemctl daemon-reload && systemctl enable --now wash-login.service`
- openrc: `rc-update add wash-login default && rc-service wash-login start`

postrm / %preun / pre-deinstall: stop the service; on purge only,
remove user/group/secret.key.

**Trust-check gotcha.** wash-priv's reservedID is refused by the
registry unless the binary is "owned by uid 0 + mode 0755" (see
`internal/router/registry.go`'s `isTrustedBinary`). The package's
install step must land `/usr/bin/wash-priv` as `root:root 0755` so
the production path doesn't need `WASH_TRUSTED_APPS_DIRS`. Verify in
the WS-C test stage.

### WS-C — matrix runner

Ferrite-shape, repo-rooted under `packaging/`:

```
packaging/
  Dockerfile.deb        # ubuntu, debian
  Dockerfile.rpm        # fedora
  Dockerfile.apk        # alpine
  run_matrix.sh         # TARGETS array; per-row OK/FAIL summary
  build-ctx/            # ephemeral; populated by run_matrix.sh
                        # (source tarball + spec files staged here)
```

Each Dockerfile is two-stage:

- **Stage 1 (build)** — install the format's build tooling
  (`debhelper`, `rpm-build`, `alpine-sdk`), unpack the source tarball,
  invoke the packager, drop the artifact in `/pkg/`.
- **Stage 2 (test)** — clean image of the same base distro. Install
  the package via the native pkg-mgr (`apt-get install /pkg/*.deb`,
  `dnf install /pkg/*.rpm`, `apk add --allow-untrusted /pkg/*.apk`).
  Run the distro-integration test stage (below).

`run_matrix.sh` TARGETS rows, format `(tag, platform, BASE, kind)`:

```
ubuntu-24.04-amd64    linux/amd64    ubuntu:24.04   deb
debian-12-amd64       linux/amd64    debian:12      deb
fedora-40-amd64       linux/amd64    fedora:40      rpm
alpine-3.21-amd64     linux/amd64    alpine:3.21    apk
```

Same `set -e` / `set +e` discipline as ferrite: fatal through
host-side staging, per-row failures tolerated through the matrix loop
so one bad distro doesn't mask the rest. Override the matrix with
`WASH_PKG_TARGETS=$'<rows>\\n…'` for a fast local sanity check.

Multi-arch (arm64, riscv64) added as new rows once amd64 is green;
needs `qemu-user-static` + `binfmt_misc` on the host.

### WS-C test stage — what runs

Three layers, in order:

1. **Static smoke** (ferrite-style, ~5 lines):

   ```
   test -x /usr/bin/wash-router
   test -x /usr/bin/wash-login
   test -f /usr/lib/systemd/system/wash-login.service  # or /etc/init.d
   getent passwd wash-system >/dev/null
   stat -c '%U %a' /usr/bin/wash-priv | grep -q '^root 755$'
   ```

2. **`go test` (cross-distro)** — pulls the Go toolchain into the
   test stage and runs `go test ./...`. Picks up the parser tests
   for every backend; passes identically on all four distros. Caching
   note: this layer pulls a Go install per container, so it's the
   slow one — cache the layer aggressively.

3. **Distro-integration tests** — `go test -tags=distro_integration
   ./apps/packages/be ./apps/services/be`. These exercise `Detect()`
   against the host's real binaries, assert the right backend wins,
   and check the action-argv helpers shell out cleanly to the real
   apt/dnf/apk/systemctl/rc-service.

   Each backend's integration test picks a known-safe operation
   (e.g. `apt-get -s install tree` simulate-only) so the test stage
   never installs random packages or restarts random services.

4. **Daemon boot** — `wash-router --listen 127.0.0.1:11000` for a
   couple of seconds; curl `/`. Proves the package's wiring is
   intact end-to-end without involving a browser.

systemd distros need `--privileged --cgroupns=host` for stage 2 if
the wash-login.service is to actually start. Alpine's openrc-in-
container is friendlier; runs unprivileged.

## Decisions locked

| Decision | Choice |
|---|---|
| Matrix scope | Unit + distro-integration Go tests + install smoke. **No Playwright in the matrix.** |
| Host e2e suite | Unchanged. Stubbed PATH dev loop stays as-is. |
| Packaging tool | Native per-format (`dpkg-buildpackage`, `rpmbuild`, `abuild`). Not nfpm. |
| Build inside container? | **Yes** — sanitized build env (`Dockerfile.build` → `Dockerfile.binaries`). Pinned Go/Node/pnpm, never the host toolchain. |
| Backends required | apt (exists), dnf (new), apk (new), OpenWRT tgz. |
| Distro list | alpine 3.21, debian 12, fedora 40, ubuntu 24.04, openwrt 24.10. |
| Arch list | amd64 (full verify), arm64 + riscv64 (deb/rpm; built cross, verify skipped without binfmt). |
| Optional display package | `wash-display` (deb/rpm), gated by `WASH_PKG_DISPLAY=1`; vendored wlroots 0.17.4 bundled; ubuntu 24.04 / fedora 40 / debian 13 (wayland ≥ 1.22) × amd64/arm64/riscv64. NOT debian 12, NOT alpine. |
| Init systems exercised | systemd (debian/fedora/ubuntu), openrc (alpine). |
| Smoke target package | `tree` — present in all four repos, removable, no side effects. |
| User/group | `wash-system:wash`, created in postinst, removed only on purge. |
| Secret key | Lazy: generated on first wash-login start. Not in postinst. |
| TLS | Out of scope. Per MULTIUSER.md, wash delegates TLS to nginx/Caddy. |

## Decisions deferred

| Item | Reason |
|---|---|
| arm64/riscv64 verify boot-smoke | Built cross on amd64; running the foreign-arch binary needs qemu-user binfmt. Rows build the package and skip the smoke without it. |
| wash-display on arm64 | Declared (`Architecture: amd64 arm64`) but not built in the matrix — the C++ compositor needs a native (qemu-emulated) arm64 build. amd64 first. |
| Fully-offline build | Network is allowed during the in-container build today; `go mod vendor` + a vendored pnpm store is a later hardening step. |
| procd / OpenWRT init | OpenWRT row ships a binary tgz; no native procd init script yet. |
| Repo publishing (apt repo, copr, alpine community) | Out of v1. `dist/packages/<tag>/` artifacts are enough. |
| Convergence with wash-vm | Different problem (architecture portability vs. distro portability). |
| Auto-update / apt pinning / dnf module setup | Not v1. |
| Playwright in the matrix | The FE/wire is distro-agnostic. Adding chromium-per-distro burns CPU for no signal. |
| Driver-container e2e split | Same reasoning. |

## Known sharp edges

1. **systemd-in-container** needs `--privileged --cgroupns=host`. GH
   Actions hosted runners don't permit `--privileged`. Self-hosted
   runner is the realistic CI story; locally `docker run --privileged`
   is fine.

2. **`getent`/`useradd` in the rpm scriptlet on minimal fedora**
   pulls in `shadow-utils`. List it as a `Requires:` so the postinst
   doesn't fail on the leanest container images.

3. **Alpine `setcap`** lives in `libcap-utils` (not installed by
   default). APKBUILD must depend on it; abuild's `post-install` runs
   `setcap` and would otherwise fail silently.

4. **systemd-tmpfiles** for `/run/wash/`. systemd distros want a
   `wash.conf` under `/usr/lib/tmpfiles.d/` so the per-uid `/run/wash`
   tree gets created with the right mode on boot. Openrc handles it
   in the initd script.

5. **`go test` in the test stage pulls a toolchain**, ~250MB per
   image. Cache aggressively; consider a shared base image
   `wash-matrix-base:<distro>` that bakes the toolchain once and is
   re-used across rows.

6. **wash-priv trust check.** Confirm `internal/router/registry.go`'s
   `isTrustedBinary` accepts `/usr/bin/wash-priv` when it's installed
   `root:root 0755` (the production path). Today the trust branch
   accepts "uid 0 + mode 0755" without env vars; this needs end-to-
   end verification in the matrix.

## Implementation milestones

### M1 — new backends

- `dnf.go`, `apk.go`, plus parser unit tests.
- `Detect()` extended.
- Host-runnable. No container work yet.

**Exit:** `go test ./apps/packages/be/...` passes on the dev box;
running wash on a fedora box renders a populated packages app.

### M2 — distro-integration test scaffolding

- New `//go:build distro_integration` test files for apt/dnf/apk/
  systemd/openrc.
- Each picks a known-safe op against the host's real binary.
- `go test -tags=distro_integration ./...` passes locally where
  the host has the relevant binary, skips where it doesn't.

**Exit:** running on the dev box hits the apt + systemd integration
tests and skips dnf/apk/openrc cleanly.

### M3 — native package source trees

- `debian/`, `rpm/`, `alpine/` populated with the full install
  layout, postinst chain, and the wash-login.service / .initd units.
- `dpkg-buildpackage`, `rpmbuild`, `abuild` succeed locally against
  a fresh tree.

**Exit:** `dist/packages/wash_<ver>.deb` etc. exist locally, install
on a matching distro, daemon comes up, `getent passwd wash-system`
returns the user.

### M4 — matrix runner

- `packaging/Dockerfile.{deb,rpm,apk}`, two-stage shape.
- `packaging/run_matrix.sh` with the 4-row TARGETS.
- Test stage runs static smoke + `go test` + distro-integration +
  daemon-boot.

**Exit:** `./packaging/run_matrix.sh` produces 4 artefacts and prints
4 × OK in the summary on a clean host with docker.

### M5 — CI wiring

- Self-hosted runner workflow (or local-only `make matrix` target).
- Trace + log extraction to `dist/matrix/<tag>/`.

**Exit:** matrix runs on every PR (or every push to main, depending
on runner availability).

## File / dir summary

```
apps/packages/be/
  dnf.go                       # NEW
  apk.go                       # NEW
  dnf_test.go                  # NEW (parser unit)
  apk_test.go                  # NEW (parser unit)
  apt_distro_test.go           # NEW (//go:build distro_integration)
  dnf_distro_test.go           # NEW
  apk_distro_test.go           # NEW

apps/services/be/
  systemd_distro_test.go       # NEW
  openrc_distro_test.go        # NEW

debian/                        # NEW tree
rpm/                           # NEW tree
alpine/                        # NEW tree

packaging/
  make-source-tarball.sh       # SOURCE-only tarball (git ls-files based)
  Dockerfile.build             # sanitized build env: Go/Node/pnpm (+display)
  Dockerfile.binaries          # compile out/ in that env, per GOARCH
  Dockerfile.deb               # ubuntu, debian   (PKG_ARCH, WASH_DISPLAY)
  Dockerfile.rpm               # fedora           (PKG_ARCH, WASH_DISPLAY)
  Dockerfile.apk               # alpine
  Dockerfile.openwrt           # openwrt (binary tgz)
  run_matrix.sh                # (tag base kind goarch display) rows
  build-ctx/                   # gitignored; populated by run_matrix.sh

Makefile                       # linux-{amd64,arm64,riscv64}, packages target
```

## Glossary

- **matrix row** — one (distro, arch, format) combination. v1 has 4
  rows, all amd64.
- **build-ctx** — ephemeral dir populated by `run_matrix.sh` with the
  source tarball and any per-format spec file. Passed as the
  `docker build` context.
- **distro-integration test** — a Go test gated on
  `//go:build distro_integration` that exercises a real distro
  binary (`apt-get`, `dnf`, `apk`, `systemctl`, `rc-service`).
  Skipped on dev boxes; unconditionally run in the matrix test
  stages.
- **smoke** — the ferrite-style "test -x / test -f / daemon boots"
  check that proves the package's wiring is intact.
