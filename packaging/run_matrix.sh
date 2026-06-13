#!/usr/bin/env bash
# Build wash packages across (distro, arch, kind) and verify each — all in
# Docker, nothing compiled on the host.
#
# Pipeline:
#   1. make-source-tarball.sh                  → SOURCE-only tarball.
#   2. Dockerfile.build  (per distro)          → wash-build:<tag>, the
#        sanitized build env (pinned Go + Node + pnpm; + the vendored-wlroots
#        build deps when WITH_DISPLAY=1).
#   3. Dockerfile.binaries (per arch, amd64    → compiles the pure-Go core
#        host, cross GOARCH — it's static          (FE + Go) into out/. Built
#        so this is fast and identical              ONCE per arch, shared by
#        across distros)                            every row of that arch.
#   4. Dockerfile.display (per distro+arch,    → builds wash-display + the
#        buildx --platform + qemu — C++/           vendored wlroots 0.17.4
#        wlroots can't cross-compile)               natively for the target
#                                                   arch; folded into the bintar.
#   5. Dockerfile.<kind> (buildx --platform)   → packages source+out into a
#        → .deb/.rpm/.apk/.tgz, then the test       native-arch package and
#        stage installs it and runs the smoke        runs the install smoke
#        + distro-integration tests (emulated         under qemu (so arm64/
#        for non-amd64).                              riscv64 get real verify).
#
# Env:
#   WASH_PKG_VERSION    default 0.8.0
#   WASH_PKG_TARGETS    newline-separated override of the row list
#   WASH_PKG_DISPLAY=1  also build the wash-display display rows
#   WASH_PKG_NO_BUILD=1 reuse already-staged tarballs/artifacts (skip compile)
#   CORE_BUILDER_BASE   distro image for the (distro-independent) core build;
#                       default ubuntu:24.04

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

docker info >/dev/null 2>&1 || { echo "docker not usable" >&2; exit 1; }
docker buildx version >/dev/null 2>&1 || { echo "docker buildx required (multi-arch builds)" >&2; exit 1; }

VERSION="${WASH_PKG_VERSION:-0.8.0}"
CORE_BUILDER_BASE="${CORE_BUILDER_BASE:-ubuntu:24.04}"
BUILD_CTX="$ROOT/packaging/build-ctx"
DIST="$ROOT/dist"
PKG_DIR="$DIST/packages"
SRC_TARBALL="$BUILD_CTX/wash_${VERSION}.src.tar.xz"

# (tag  base  kind  goarch  display)  kind ∈ {deb,rpm,apk,openwrt}.
# Display rows (display=1) are skipped unless WASH_PKG_DISPLAY=1, and only
# target wlroots-buildable distros (wayland-server >= 1.22): ubuntu 24.04,
# fedora 40, debian 13 (trixie) — NOT debian 12 (wayland 1.21). Alpine =
# core only (no display) per design.
TARGETS=(
  "ubuntu-24.04-amd64      ubuntu:24.04                    deb      amd64    0"
  "ubuntu-24.04-arm64      ubuntu:24.04                    deb      arm64    0"
  "ubuntu-24.04-riscv64    ubuntu:24.04                    deb      riscv64  0"
  "debian-13-amd64         debian:13                       deb      amd64    0"
  "debian-13-arm64         debian:13                       deb      arm64    0"
  "debian-13-riscv64       debian:13                       deb      riscv64  0"
  "fedora-40-amd64         fedora:40                       rpm      amd64    0"
  "fedora-40-arm64         fedora:40                       rpm      arm64    0"
  # NOTE: no fedora riscv64 — Fedora publishes no riscv64 container image,
  # so buildx --platform linux/riscv64 has no base to build on. (ubuntu /
  # debian DO ship riscv64 images, so those riscv64 rows work.)
  "alpine-3.21-amd64       alpine:3.21                     apk      amd64    0"
  "alpine-3.21-arm64       alpine:3.21                     apk      arm64    0"
  "alpine-3.21-riscv64     alpine:3.21                     apk      riscv64  0"
  # ---- optional native wash-display (WASH_PKG_DISPLAY=1) ----
  # amd64 + arm64 only — wash-display is not built for riscv64 (debian/control
  # ships it Architecture: amd64 arm64). The riscv64 build image has no Node,
  # and `make out/wash-display` pulls vendor-sync → web-deps → pnpm, so a
  # riscv64-display row fails; riscv64 gets the core package, no display subpkg.
  "ubuntu-24.04-amd64-display    ubuntu:24.04  deb  amd64    1"
  "ubuntu-24.04-arm64-display    ubuntu:24.04  deb  arm64    1"
  "debian-13-amd64-display       debian:13     deb  amd64    1"
  "debian-13-arm64-display       debian:13     deb  arm64    1"
  "fedora-40-amd64-display       fedora:40     rpm  amd64    1"
  "fedora-40-arm64-display       fedora:40     rpm  arm64    1"
)
if [[ -n "${WASH_PKG_TARGETS:-}" ]]; then
  mapfile -t TARGETS <<<"${WASH_PKG_TARGETS}"
fi

slug() { echo "$1" | tr ':/' '--'; }
bintar_path() { echo "$BUILD_CTX/bintar-$1.tar.xz"; }

# ----- host-side staging (set -e) -----
set -e
mkdir -p "$BUILD_CTX" "$DIST" "$PKG_DIR"

if [[ "${WASH_PKG_NO_BUILD:-}" != "1" ]]; then
  echo ">>> 1. Source tarball"
  WASH_PKG_VERSION="$VERSION" "$ROOT/packaging/make-source-tarball.sh" >/dev/null
  mv -f "$DIST/wash_${VERSION}.tar.xz" "$SRC_TARBALL"
  echo "    staged $(du -h "$SRC_TARBALL" | cut -f1) (source)"
fi
trap 'docker container prune -f >/dev/null 2>&1 || true' EXIT

# build_core <goarch> — compile the pure-Go core for <goarch> (amd64 host,
# cross). Produces $(bintar_path core-<goarch>) = source + core out/.
build_core() {
  local goarch="$1" key="core-$1"
  local bintar; bintar="$(bintar_path "$key")"
  [[ "${WASH_PKG_NO_BUILD:-}" == "1" && -f "$bintar" ]] && { echo "    reuse $bintar"; return 0; }

  local builder="wash-build:$(slug "$CORE_BUILDER_BASE")"
  echo ">>> build-env  $builder"
  docker build -f "$ROOT/packaging/Dockerfile.build" \
      --build-arg BASE="$CORE_BUILDER_BASE" --build-arg WITH_DISPLAY=0 \
      -t "$builder" "$ROOT/packaging" || return 1

  local bin_tag="wash-binaries:$key"
  echo ">>> compile    $bin_tag  (GOARCH=$goarch, core)"
  docker build -f "$ROOT/packaging/Dockerfile.binaries" \
      --build-arg BUILDER="$builder" --build-arg VERSION="$VERSION" \
      --build-arg GOARCH="$goarch" -t "$bin_tag" "$BUILD_CTX" || return 1

  local tmp; tmp="$(mktemp -d)"; local srcdir="$tmp/wash-${VERSION}"
  mkdir -p "$srcdir/out"
  tar -xJf "$SRC_TARBALL" --strip-components=1 -C "$srcdir" || { rm -rf "$tmp"; return 1; }
  local cid; cid="$(docker create "$bin_tag")" || { rm -rf "$tmp"; return 1; }
  docker cp "$cid:/src/out/." "$srcdir/out/" >/dev/null || { docker rm "$cid">/dev/null 2>&1; rm -rf "$tmp"; return 1; }
  docker rm "$cid" >/dev/null
  tar -cJf "$bintar" -C "$tmp" "wash-${VERSION}" || { rm -rf "$tmp"; return 1; }
  rm -rf "$tmp"
  echo "    staged $(du -h "$bintar" | cut -f1) (source+core)"
}

# build_display <base> <goarch> — build wash-display + vendored wlroots for
# the target arch in a native (buildx/qemu) container. Stages the binary +
# bundled lib under $BUILD_CTX/disp-<key>/.
build_display() {
  local base="$1" goarch="$2" key="$(slug "$1")-$2-display"
  local dispdir="$BUILD_CTX/disp-$key"
  # Reuse the staged native build unless forced — the wlroots+wash-display
  # compile is the expensive (emulated) step and depends only on the C/C++
  # source, not on packaging metadata. WASH_PKG_REBUILD_DISPLAY=1 forces it.
  [[ "${WASH_PKG_REBUILD_DISPLAY:-}" != "1" && -x "$dispdir/wash-display" ]] && { echo "    reuse $dispdir (staged)"; return 0; }

  local plat="linux/$goarch"
  local builder="wash-build-disp:$(slug "$base")-$goarch"
  echo ">>> build-env  $builder  (display, $plat)"
  docker buildx build --platform "$plat" --load -f "$ROOT/packaging/Dockerfile.build" \
      --build-arg BASE="$base" --build-arg WITH_DISPLAY=1 \
      -t "$builder" "$ROOT/packaging" || return 1

  local dispimg="wash-display-bin:$(slug "$base")-$goarch"
  echo ">>> compile    $dispimg  (wash-display + vendored wlroots, $plat)"
  # FE_BUILDER is the amd64 core builder (has Node) — it builds the
  # arch-independent panel.js on the build host; BUILDER (target arch) does
  # the native wlroots + wash-display compile.
  docker buildx build --platform "$plat" --load -f "$ROOT/packaging/Dockerfile.display" \
      --build-arg FE_BUILDER="wash-build:$(slug "$CORE_BUILDER_BASE")" \
      --build-arg BUILDER="$builder" --build-arg VERSION="$VERSION" \
      -t "$dispimg" "$BUILD_CTX" || return 1

  rm -rf "$dispdir"; mkdir -p "$dispdir/lib"
  local cid; cid="$(docker create --platform "$plat" "$dispimg")" || return 1
  docker cp "$cid:/src/out/wash-display" "$dispdir/wash-display" >/dev/null || { docker rm "$cid">/dev/null 2>&1; return 1; }
  docker cp "$cid:/src/out/lib/." "$dispdir/lib/" >/dev/null || { docker rm "$cid">/dev/null 2>&1; return 1; }
  docker rm "$cid" >/dev/null
  echo "    staged $dispdir ($(file -b "$dispdir/wash-display" | cut -d, -f1-2))"
}

# assemble_display_bintar <goarch> <base> — core bintar + display artifacts
# → a per-row source+out tarball. Echoes its path.
assemble_display_bintar() {
  local goarch="$1" base="$2"
  local core; core="$(bintar_path "core-$goarch")"
  local dispdir="$BUILD_CTX/disp-$(slug "$base")-$goarch-display"
  local out="$BUILD_CTX/bintar-$(slug "$base")-$goarch-display.tar.xz"
  local tmp; tmp="$(mktemp -d)"
  tar -xJf "$core" -C "$tmp"
  cp "$dispdir/wash-display" "$tmp/wash-${VERSION}/out/wash-display"
  mkdir -p "$tmp/wash-${VERSION}/out/lib"
  cp -P "$dispdir"/lib/* "$tmp/wash-${VERSION}/out/lib/"
  tar -cJf "$out" -C "$tmp" "wash-${VERSION}"
  rm -rf "$tmp"
  echo "$out"
}

# ----- matrix loop (set +e) -----
set +e

# ----- parallel matrix -----
# Rows are independent docker builds. The only shared mutable state in the old
# serial loop was $BUILD_CTX/wash_${VERSION}.tar.xz (clobbered per row), so we
# give each row its OWN context dir (ctx-<tag>/ holding just the tarball — the
# package Dockerfiles COPY only wash_<ver>.tar.xz). Shared per-arch core builds
# and per-(distro,arch) display builds are hoisted into barrier'd phases so they
# run once, not per row.
#
# WASH_PKG_JOBS bounds concurrency (default 4). The heavy rows are emulated
# arm64/riscv64 builds whose meson/ninja already saturate cores internally, so a
# handful in flight keeps the box busy without thrashing.
JOBS="${WASH_PKG_JOBS:-4}"
gate() { while (( $(jobs -rp | wc -l) >= JOBS )); do wait -n; done; }

# enabled rows (skip display rows unless opted in).
# WASH_PKG_ROWS=<space/comma list of tag globs> builds only matching rows — the
# per-(arch,distro,pkg) make leaves set it to a single tag (e.g.
# "ubuntu-24.04-amd64" or "fedora-40-amd64-display"). A matched display row is
# included even without WASH_PKG_DISPLAY=1 (asking for it IS opting in).
rows=()
want_rows="${WASH_PKG_ROWS:-}"; want_rows="${want_rows//,/ }"
row_wanted() {  # $1=tag ; true if no filter, or tag matches a glob in want_rows
    [[ -z "$want_rows" ]] && return 0
    local pat; for pat in $want_rows; do [[ "$1" == $pat ]] && return 0; done
    return 1
}
for row in "${TARGETS[@]}"; do
    read -r tag base kind goarch display <<<"$row"
    [[ -z "$tag" ]] && continue
    row_wanted "$tag" || continue
    [[ "$display" == "1" && -z "$want_rows" && "${WASH_PKG_DISPLAY:-}" != "1" ]] && continue
    rows+=("$tag|$base|$kind|$goarch|$display")
done
if [[ -n "$want_rows" && ${#rows[@]} -eq 0 ]]; then
    echo "run_matrix: WASH_PKG_ROWS='$WASH_PKG_ROWS' matched no rows" >&2; exit 2
fi

# process_row <tag|base|kind|goarch|display> — stages 2-4 for one row in its own
# build context. Writes a one-line verdict to $DIST/<tag>.result and build
# output to $DIST/<tag>.log. Assumes its arch core (+ display) is already built.
process_row() {
    local tag base kind goarch display
    IFS='|' read -r tag base kind goarch display <<<"$1"
    local log="$DIST/${tag}.log" res="$DIST/${tag}.result"
    : > "$log"
    local bintar
    if [[ "$display" == "1" ]]; then
        bintar="$(assemble_display_bintar "$goarch" "$base" 2>>"$log")"
    else
        bintar="$(bintar_path "core-$goarch")"
    fi
    [[ -f "$bintar" ]] || { echo "$tag: NO BINTAR (see $log)" >"$res"; return; }

    # Per-row context so parallel rows don't clobber a shared tarball.
    local ctx="$BUILD_CTX/ctx-$tag"
    rm -rf "$ctx"; mkdir -p "$ctx"
    cp -f "$bintar" "$ctx/wash_${VERSION}.tar.xz"

    local image="wash-pkg:$tag" pkg_out="$PKG_DIR/$tag"
    # Distro-integration tests (apt/dnf/apk parsing) are arch-independent and
    # slow/flaky under qemu — run them only on the native amd64 rows.
    local run_distro_tests=0; [[ "$goarch" == "amd64" ]] && run_distro_tests=1
    if ! docker buildx build --platform "linux/$goarch" --load \
            -f "$ROOT/packaging/Dockerfile.$kind" \
            --build-arg BASE="$base" --build-arg VERSION="$VERSION" \
            --build-arg WASH_DISPLAY="$display" \
            --build-arg RUN_DISTRO_TESTS="$run_distro_tests" \
            -t "$image" "$ctx" >>"$log" 2>&1; then
        echo "$tag: PACKAGE FAIL (see $log)" >"$res"; rm -rf "$ctx"; return
    fi
    rm -rf "$ctx"

    rm -rf "$pkg_out"; mkdir -p "$pkg_out"
    local cid; cid="$(docker create --platform "linux/$goarch" "$image" 2>>"$log")"
    docker cp "$cid:/pkg/." "$pkg_out/" >/dev/null 2>&1 || true
    docker rm "$cid" >/dev/null 2>&1 || true
    local artifact; artifact="$(find "$pkg_out" -maxdepth 1 \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' -o -name '*.tgz' \) | sort | head -1)"
    if [[ -n "$artifact" ]]; then
        echo "$tag: OK  $(du -h "$artifact" | cut -f1)  $(basename "$artifact")" >"$res"
    else
        echo "$tag: BUILD_OK but NO ARTIFACT (see $log)" >"$res"
    fi
}

# Phase A: shared core build-env once, so parallel build_core calls cache-hit it
# instead of racing N identical `docker build -t wash-build:...`.
echo ">>> [phase A] core build-env  $CORE_BUILDER_BASE"
docker build -f "$ROOT/packaging/Dockerfile.build" \
    --build-arg BASE="$CORE_BUILDER_BASE" --build-arg WITH_DISPLAY=0 \
    -t "wash-build:$(slug "$CORE_BUILDER_BASE")" "$ROOT/packaging" \
    >"$DIST/phase-build-env.log" 2>&1 \
    || { echo "[matrix] core build-env FAILED (see $DIST/phase-build-env.log)"; exit 1; }

# Phase B: per-arch core (parallel).
echo ">>> [phase B] cores (parallel x$JOBS): $(printf '%s\n' "${rows[@]}" | cut -d'|' -f4 | sort -u | tr '\n' ' ')"
for arch in $(printf '%s\n' "${rows[@]}" | cut -d'|' -f4 | sort -u); do
    gate
    ( build_core "$arch" >"$DIST/phase-core-$arch.log" 2>&1; echo $? >"$DIST/phase-core-$arch.rc" ) &
done
wait

# Phase C: per-(distro,arch) display (parallel) — display rows only.
if [[ "${WASH_PKG_DISPLAY:-}" == "1" ]]; then
    echo ">>> [phase C] displays (parallel x$JOBS)"
    declare -A seen_disp=()
    for r in "${rows[@]}"; do
        IFS='|' read -r tag base kind goarch display <<<"$r"
        [[ "$display" == "1" ]] || continue
        dkey="$(slug "$base")-$goarch"
        [[ -n "${seen_disp[$dkey]:-}" ]] && continue
        seen_disp[$dkey]=1
        gate
        ( build_display "$base" "$goarch" >"$DIST/phase-disp-$dkey.log" 2>&1; echo $? >"$DIST/phase-disp-$dkey.rc" ) &
    done
    wait
fi

# Phase D: per-row package build (parallel, isolated contexts).
echo ">>> [phase D] package rows (parallel x$JOBS): ${#rows[@]} rows"
for r in "${rows[@]}"; do
    gate
    ( process_row "$r" ) &
done
wait

# ----- summary (in TARGETS order) -----
echo
echo "════════════════════════════════════════════════════════════"
echo "[matrix] SUMMARY  (jobs=$JOBS)"
echo "════════════════════════════════════════════════════════════"
fail=0
for r in "${rows[@]}"; do
    IFS='|' read -r tag _ _ _ _ <<<"$r"
    line="$(cat "$DIST/${tag}.result" 2>/dev/null)"
    [[ -z "$line" ]] && line="$tag: NO RESULT (see $DIST/${tag}.log)"
    echo "  $line"
    case "$line" in *FAIL*|*"NO RESULT"*|*"NO BINTAR"*|*"NO ARTIFACT"*) fail=1;; esac
done
echo; echo "Packages: $PKG_DIR/<tag>/   Logs: $DIST/<tag>.log"
exit $fail
