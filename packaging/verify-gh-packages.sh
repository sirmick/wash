#!/usr/bin/env bash
# verify-gh-packages.sh — download the CI-built native packages from GitHub
# Actions and install + boot-smoke each one in a CLEAN matching-distro
# container.
#
# Why: our build-time smoke runs INSIDE the build image (which has Go/Node +
# build deps) using locally-built bytes. This instead installs the actual
# GH-uploaded artifact on a PRISTINE runtime image — proving the bytes are
# intact, the runtime deps are declared correctly, and the postinst hooks
# (wash-system user, /etc/wash, wash-priv mode) run on a fresh system.
#
# Only amd64 is on GH (ci builds amd64). arm64/riscv64 packages exist locally
# via `make all-package`.
#
# Usage:
#   packaging/verify-gh-packages.sh [ROWS...]        # default: all 4
#   packaging/verify-gh-packages.sh ubuntu24 alpine321
# Env:
#   WASH_PKGTEST_RUN   ci.yml run id to pull (default: latest successful on this branch)
#   WASH_PKGTEST_DIR   work dir (default: /tmp/wash-pkgtest)
#   WASH_PKGTEST_KEEP=1  reuse already-downloaded artifacts (skip re-download)
#
# Rows: ubuntu24 debian13 fedora40 alpine321
set -euo pipefail

WORK="${WASH_PKGTEST_DIR:-/tmp/wash-pkgtest}"
DL="$WORK/dl"
ALL_ROWS="ubuntu24 debian13 fedora40 alpine321"
ROWS="${*:-$ALL_ROWS}"
ROWS="${ROWS//,/ }"

command -v docker >/dev/null || { echo "docker required" >&2; exit 2; }
command -v gh >/dev/null || { echo "gh (authenticated) required" >&2; exit 2; }

# --- resolve the run + download its package artifacts ---------------------
RID="${WASH_PKGTEST_RUN:-}"
if [ -z "$RID" ]; then
  RID=$(gh run list --workflow ci.yml --status success --limit 1 --json databaseId --jq '.[0].databaseId')
fi
echo "▸ ci run: $RID"

have_pkgs() { [ -n "$(find "$DL" \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) 2>/dev/null | head -1)" ]; }
if [ "${WASH_PKGTEST_KEEP:-0}" = "1" ] && have_pkgs; then
  echo "▸ reusing artifacts in $DL"
else
  rm -rf "$DL"; mkdir -p "$DL"
  echo "▸ downloading *-wash-package → $DL"
  gh run download "$RID" --dir "$DL" --pattern '*-wash-package'
fi

row_meta() { # -> "<docker-image> <pkgmgr>"
  case "$1" in
    ubuntu24)  echo "ubuntu:24.04 apt" ;;
    debian13)  echo "debian:13 apt" ;;
    fedora40)  echo "fedora:40 dnf" ;;
    alpine321) echo "alpine:3.21 apk" ;;
    *) return 1 ;;
  esac
}

# In-container smoke (POSIX sh; $1=wash pkg basename, $2=pkgmgr, $3=wash-login
# pkg basename or ""). Mirrors the build-time Dockerfile smoke: static checks +
# `-version` + boot/serve/kill for wash, then wash-login on top and what its
# postinst owns. The split matters: core wash runs as the invoking user and
# creates no system user (debian/wash.postinst); the wash group, wash-system
# and /etc/wash are wash-login's.
read -r -d '' SMOKE <<'EOS' || true
set -eu
PKG="$1"; MGR="$2"; LOGIN="${3:-}"; F="/pkg/$PKG"
echo "  install ($MGR): $PKG"
case "$MGR" in
  apt) export DEBIAN_FRONTEND=noninteractive
       apt-get update -qq
       apt-get install -y -qq "$F" wget ca-certificates >/dev/null ;;
  dnf) dnf install -y -q "$F" wget >/dev/null ;;
  apk) apk add --no-cache --allow-untrusted "$F" wget >/dev/null ;;
esac
echo "  static: binaries, wash-priv trust anchor"
test -x /usr/bin/wash-router
test -x /usr/bin/wash-priv
# wash-priv is a symlink to the multicall binary; the router's trust gate
# stats THROUGH it, so the anchor is the target: root, 0755.
[ "$(stat -L -c '%U %a' /usr/bin/wash-priv)" = "root 755" ]
echo "  -version"; /usr/bin/wash-router -version
echo "  boot: serve HTTP on loopback"
mkdir -p /tmp/empty-apps
/usr/bin/wash-router -http -no-auth -listen 127.0.0.1:11000 -apps-dir /tmp/empty-apps >/tmp/router.log 2>&1 &
R=$!
ok=0
for _ in $(seq 1 15); do
  if wget -q -O /dev/null --tries=1 --timeout=1 http://127.0.0.1:11000/ 2>/dev/null; then ok=1; break; fi
  sleep 1
done
kill $R 2>/dev/null || true; wait $R 2>/dev/null || true
if [ "$ok" != 1 ]; then echo "  BOOT FAIL"; echo '--- router.log ---'; cat /tmp/router.log; exit 1; fi
if [ -n "$LOGIN" ]; then
  echo "  install ($MGR): $LOGIN"
  L="/pkg/$LOGIN"
  case "$MGR" in
    apt) apt-get install -y -qq "$L" >/dev/null ;;
    dnf) dnf install -y -q "$L" >/dev/null ;;
    apk) apk add --no-cache --allow-untrusted "$L" >/dev/null ;;
  esac
  echo "  static (wash-login): binary, wash group, wash-system user, /etc/wash"
  test -x /usr/bin/wash-login
  grep -q '^wash-system:' /etc/passwd
  grep -q '^wash:' /etc/group
  test -d /etc/wash
fi
echo "  SMOKE-OK"
EOS

PASS=""; FAIL=""
for row in $ROWS; do
  meta=$(row_meta "$row") || { echo "unknown row: $row" >&2; exit 2; }
  # shellcheck disable=SC2086
  set -- $meta; IMAGE=$1; MGR=$2
  pkgdir="$DL/amd64-${row}-wash-package"
  # The wash package, not wash-login: the artifact carries both, wash-login
  # depends on wash, and the smoke boots the router wash ships. A bare
  # `head -1` picked whichever find listed first, so a row failed with
  # "nothing provides wash" whenever wash-login came first.
  pkgfile=$(find "$pkgdir" -maxdepth 2 -type f \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) ! -name 'wash-login*' 2>/dev/null | head -1 || true)
  echo
  echo "════ $row → $IMAGE ════"
  if [ -z "$pkgfile" ]; then echo "  ✗ no package under $pkgdir"; FAIL="$FAIL $row"; continue; fi
  echo "  pkg: $(basename "$pkgfile")"
  loginfile=$(find "$(dirname "$pkgfile")" -maxdepth 1 -type f -name 'wash-login*' \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) 2>/dev/null | head -1 || true)
  [ -n "$loginfile" ] && echo "  pkg: $(basename "$loginfile")"
  # --platform: the packages are amd64, and a host that has built an arm64 row
  # holds the arm64 variant of the same tag (debian:13), which docker would
  # otherwise run here — dpkg then refuses the amd64 .deb.
  if docker run --rm --platform linux/amd64 -v "$(dirname "$pkgfile")":/pkg:ro "$IMAGE" sh -c "$SMOKE" sh "$(basename "$pkgfile")" "$MGR" "$(basename "${loginfile:-}")"; then
    echo "  ✓ $row PASS"; PASS="$PASS $row"
  else
    echo "  ✗ $row FAIL"; FAIL="$FAIL $row"
  fi
done

echo
echo "════ verify-gh-packages summary (run $RID) ════"
echo "  pass:${PASS:- none}"
echo "  fail:${FAIL:- none}"
[ -z "$FAIL" ] || { echo "PACKAGE VERIFY FAILED"; exit 1; }
echo "ALL GH PACKAGES INSTALL + BOOT ✓"
