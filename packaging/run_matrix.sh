#!/usr/bin/env bash
# Build wash packages across (distro, arch) and verify each via the
# corresponding Dockerfile's test stage.
#
# Per-row sequence:
#   1. docker build the two-stage image (build → test). The test
#      stage's last RUN is the daemon-boot smoke; a failing build
#      means a failing row.
#   2. extract the produced .deb/.rpm/.apk from the image into
#      dist/packages/<tag>/.
#
# All four rows run independently. set -e governs host-side staging
# (must succeed); the matrix loop is set +e so one bad distro doesn't
# mask the rest. Per-row OK/FAIL summary at the end.

set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${WASH_PKG_VERSION:-0.8.0}"
BUILD_CTX="$ROOT/packaging/build-ctx"
DIST="$ROOT/dist"
PKG_DIR="$DIST/packages"
TARBALL_NAME="wash_${VERSION}.tar.xz"

# (image-tag, platform, BASE, kind)  kind ∈ {deb, rpm, apk}.
# Override the matrix with a newline-separated WASH_PKG_TARGETS:
#   WASH_PKG_TARGETS=$'ubuntu-24.04-amd64 linux/amd64 ubuntu:24.04 deb' \
#     ./packaging/run_matrix.sh
TARGETS=(
  "ubuntu-24.04-amd64      linux/amd64    ubuntu:24.04                    deb"
  "debian-12-amd64         linux/amd64    debian:12                       deb"
  "fedora-40-amd64         linux/amd64    fedora:40                       rpm"
  "alpine-3.21-amd64       linux/amd64    alpine:3.21                     apk"
  "openwrt-24.10.6-x86_64  linux/amd64    openwrt/rootfs:x86-64-24.10.6   openwrt"
)
if [[ -n "${WASH_PKG_TARGETS:-}" ]]; then
  mapfile -t TARGETS <<<"$WASH_PKG_TARGETS"
fi

# ----- host-side staging (set -e: must succeed) -----
set -e

if [[ "${WASH_PKG_NO_BUILD:-}" != "1" ]]; then
  echo ">>> 1. Build wash-* binaries (clean standalone)"
  # Force a clean rebuild so the packager doesn't trip over
  # multicall-mode symlinks left in out/ by a prior --multicall run.
  # (`debian/rules` overrides dh_auto_clean to a no-op so the binaries
  # survive dh's clean phase, but symlinks pointing at a missing
  # `wash` multicall binary would still tank dh_install.)
  rm -rf "$ROOT/out"
  "$ROOT/build.sh" --standalone
fi

echo ">>> 2. Precompile distro_integration test binaries (static, cross-distro)"
mkdir -p "$ROOT/out"
CGO_ENABLED=0 go test -c -tags=distro_integration -trimpath \
  -o "$ROOT/out/packages_distro.test" ./apps/packages/be
CGO_ENABLED=0 go test -c -tags=distro_integration -trimpath \
  -o "$ROOT/out/services_distro.test" ./apps/services/be
chmod 0755 "$ROOT/out/packages_distro.test" "$ROOT/out/services_distro.test"

echo ">>> 3. Stage source tarball ($BUILD_CTX/$TARBALL_NAME)"
mkdir -p "$BUILD_CTX"
rm -f "$BUILD_CTX"/*.tar.xz
TMPDIR=$(mktemp -d)
trap 'rm -rf "$TMPDIR"; docker container prune -f >/dev/null 2>&1 || true' EXIT
SRCDIR="$TMPDIR/wash-${VERSION}"
mkdir -p "$SRCDIR"
# Mirror ferrite's exclude set; trim wash-vm/ (huge, irrelevant to
# packaging) and dist/ (would recurse on the artefact dir).
rsync -a \
    --exclude='node_modules/' \
    --exclude='.git/' \
    --exclude='dist/' \
    --exclude='wash-vm/' \
    --exclude='packaging/build-ctx/' \
    --exclude='e2e/test-results/' \
    --exclude='e2e/node_modules/' \
    --exclude='*.log' \
    "$ROOT/" "$SRCDIR/"
tar -cJf "$BUILD_CTX/$TARBALL_NAME" -C "$TMPDIR" "wash-${VERSION}"
echo "    staged $(du -h "$BUILD_CTX/$TARBALL_NAME" | cut -f1)"

# ----- matrix loop (set +e: per-row tolerant) -----
set +e

mkdir -p "$DIST" "$PKG_DIR"
results=()
for row in "${TARGETS[@]}"; do
    read -r tag platform base kind <<<"$row"
    image="wash-pkg:$tag"
    log="$DIST/${tag}.log"
    pkg_out="$PKG_DIR/$tag"

    echo
    echo "════════════════════════════════════════════════════════════"
    echo "[matrix] $tag  (BASE=$base, platform=$platform, kind=$kind)"
    echo "[matrix] log: $log"
    echo "════════════════════════════════════════════════════════════"

    if ! docker build \
            --platform "$platform" \
            -f "$ROOT/packaging/Dockerfile.$kind" \
            --build-arg BASE="$base" \
            --build-arg VERSION="$VERSION" \
            -t "$image" \
            "$BUILD_CTX" 2>&1 | tee "$log"; then
        results+=("$tag: FAIL  (see $log)")
        continue
    fi

    # Extract built artefact out of the test image to dist/packages/<tag>/
    rm -rf "$pkg_out"; mkdir -p "$pkg_out"
    cid="$(docker create "$image")"
    docker cp "$cid:/pkg/." "$pkg_out/" >/dev/null 2>&1 || true
    docker rm "$cid" >/dev/null 2>&1 || true
    artifact="$(find "$pkg_out" -maxdepth 1 \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' -o -name '*.tgz' \) | sort | head -1)"
    if [[ -n "$artifact" ]]; then
        size="$(du -h "$artifact" | cut -f1)"
        results+=("$tag: OK   $size $(basename "$artifact")")
    else
        results+=("$tag: BUILD_OK but NO ARTIFACT (see $log)")
    fi
done

echo
echo "════════════════════════════════════════════════════════════"
echo "[matrix] SUMMARY"
echo "════════════════════════════════════════════════════════════"
for r in "${results[@]}"; do echo "  $r"; done
echo
echo "Built images:"
docker images "wash-pkg" 2>/dev/null | head -20
echo
echo "Packages: $PKG_DIR/<tag>/"
echo "Logs:     $DIST/<tag>.log"
