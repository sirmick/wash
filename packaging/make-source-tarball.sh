#!/usr/bin/env bash
#
# make-source-tarball.sh — produce a self-contained *source* tarball that
# the deb/rpm packaging builds compile inside Docker (the "sanitized build
# environment"; see packaging/Dockerfile.build).
#
# This is the key difference from wash's older host-built flow: the tarball
# ships SOURCE ONLY — no prebuilt out/wash-* binaries. The Go binaries and
# the Vite frontends are (re)built hermetically in the container from this
# tree via `make`, so the host's Go / Node / cmake versions never leak into
# a shipped package.
#
# What goes in:
#   - Every tracked + locally-modified + untracked-not-ignored file in the
#     worktree (git ls-files). Captures uncommitted work-in-progress, which
#     `git archive HEAD` alone would miss.
#
# What stays out (via .gitignore + explicit prunes):
#   - out/, **/dist/, **/node_modules/, branches/, tmp/   (.gitignore)
#   - wash-vm/      — huge RISC-V VM subtree, irrelevant to host packages
#   - e2e/          — Playwright suite, not needed to build the binaries
#
# Output: dist/wash_<version>.tar.xz   — top dir wash-<version>/, matching
#         dpkg-buildpackage's expectation and the spec's `%autosetup -n`.
#
# Usage:
#   packaging/make-source-tarball.sh [version]
#
# version precedence: arg > $WASH_PKG_VERSION > root VERSION file >
# debian/changelog > rpm spec. The root VERSION file is the master (it also
# stamps the binaries via the Makefile); `make check-versions` guards that the
# changelog/spec/APKBUILD literals all agree with it.

set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

command -v git >/dev/null || { echo "git not installed" >&2; exit 1; }
command -v xz  >/dev/null || { echo "xz not installed (apt install xz-utils)" >&2; exit 1; }

# ----- resolve version -----------------------------------------------------
VERSION="${1:-${WASH_PKG_VERSION:-}}"
if [[ -z "$VERSION" && -f VERSION ]]; then
    VERSION="$(cat VERSION)"
fi
if [[ -z "$VERSION" ]] && command -v dpkg-parsechangelog >/dev/null 2>&1; then
    # Upstream version only (strip the Debian -N revision).
    VERSION="$(dpkg-parsechangelog -l debian/changelog -S Version 2>/dev/null | sed 's/-[^-]*$//')"
fi
if [[ -z "$VERSION" ]]; then
    VERSION="$(awk '/^Version:/ {print $2; exit}' rpm/wash.spec)"
fi
if [[ -z "$VERSION" ]]; then
    echo "ERROR: could not determine version (pass as arg or set WASH_PKG_VERSION)" >&2
    exit 1
fi

PKG="wash"
DIST="$ROOT/dist"
STAGE="$DIST/${PKG}-${VERSION}"
TARBALL="$DIST/${PKG}_${VERSION}.tar.xz"

echo "[tarball] version: $VERSION"
echo "[tarball] staging: $STAGE"

rm -rf "$STAGE" "$TARBALL"
mkdir -p "$STAGE"

# Enumerate the working tree directly (not `git archive HEAD`) so a dev's
# uncommitted changes on this branch make it into the package. -z + tar
# --null is binary-safe for paths with spaces.
echo "[tarball] exporting worktree (tracked + modified + untracked-not-ignored)..."
git ls-files --cached --modified --others --exclude-standard -z \
    | tar --null --files-from=- -cf - \
    | tar -x -C "$STAGE"

# Prune subtrees that are tracked but not needed to build the host packages.
# Keeping them only bloats the tarball + every Docker build context.
echo "[tarball] trimming..."
rm -rf "$STAGE/wash-vm"          # RISC-V VM image build (hundreds of MB)
rm -rf "$STAGE/e2e"              # Playwright e2e suite — not a build input
rm -rf "$STAGE/dist"             # never recurse onto the artefact dir

echo "[tarball] packing → $TARBALL"
tar -C "$DIST" -cJf "$TARBALL" "${PKG}-${VERSION}"
rm -rf "$STAGE"

du -h "$TARBALL" | cut -f1 | sed 's/^/[tarball] size: /'
echo "[tarball] done."
