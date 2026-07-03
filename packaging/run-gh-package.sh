#!/usr/bin/env bash
# run-gh-package.sh — install a CI-built native package in a CLEAN container
# and SERVE it on a published port, so you can browse the packaged desktop.
#
# Unlike verify-gh-packages.sh (install + boot-smoke + kill), this serves the
# full app catalog from the installed package (-apps-dir /usr/bin) on
# 0.0.0.0 and publishes the port to the host. Foreground; Ctrl-C stops and
# removes the container.
#
# Usage / env:
#   ROW=ubuntu24 PORT=11000 packaging/run-gh-package.sh
#   ROW   ubuntu24|debian13|fedora40|alpine321   (default ubuntu24)
#   PORT  host port to publish        (default 11000)
#   WASH_PKGTEST_RUN  ci run id       (default: latest successful ci.yml)
#   WASH_PKGTEST_DIR  artifact dir    (default: /tmp/wash-pkgtest)
#
# NOTE: served with -no-auth and bound 0.0.0.0 — anyone who can reach the
# published port gets the desktop. For local/LAN testing only; stop when done.
# amd64 only (that's what ci uploads).
set -euo pipefail

ROW="${ROW:-ubuntu24}"
PORT="${PORT:-11000}"
WORK="${WASH_PKGTEST_DIR:-/tmp/wash-pkgtest}"
DL="$WORK/dl"
NAME="wash-pkg-run-$ROW"

command -v docker >/dev/null || { echo "docker required" >&2; exit 2; }

case "$ROW" in
  ubuntu24)  IMG=ubuntu:24.04 MGR=apt ;;
  debian13)  IMG=debian:13    MGR=apt ;;
  fedora40)  IMG=fedora:40    MGR=dnf ;;
  alpine321) IMG=alpine:3.21  MGR=apk ;;
  *) echo "unknown ROW: $ROW (ubuntu24|debian13|fedora40|alpine321)" >&2; exit 2 ;;
esac

# Ensure the artifact is present; download just this row if missing.
find_pkg() { find "$DL/amd64-${ROW}-wash-package" -type f \( -name '*.deb' -o -name '*.rpm' -o -name '*.apk' \) 2>/dev/null | head -1; }
pkgfile=$(find_pkg || true)
if [ -z "$pkgfile" ]; then
  command -v gh >/dev/null || { echo "gh required to download (no cached artifact)" >&2; exit 2; }
  RID="${WASH_PKGTEST_RUN:-$(gh run list --workflow ci.yml --status success --limit 1 --json databaseId --jq '.[0].databaseId')}"
  mkdir -p "$DL"
  echo "▸ downloading amd64-${ROW}-wash-package from ci run $RID"
  gh run download "$RID" --dir "$DL" --pattern "amd64-${ROW}-wash-package"
  pkgfile=$(find_pkg)
fi
[ -n "$pkgfile" ] || { echo "no package found for $ROW" >&2; exit 1; }
pkgbase=$(basename "$pkgfile")

case "$MGR" in
  apt) INST="export DEBIAN_FRONTEND=noninteractive; apt-get update -qq && apt-get install -y -qq /pkg/$pkgbase" ;;
  dnf) INST="dnf install -y -q /pkg/$pkgbase" ;;
  apk) INST="apk add --no-cache --allow-untrusted /pkg/$pkgbase" ;;
esac

docker rm -f "$NAME" >/dev/null 2>&1 || true
cleanup() { echo; echo "▸ stopping $NAME"; docker rm -f "$NAME" >/dev/null 2>&1 || true; }
trap cleanup INT TERM EXIT

echo "▸ $ROW ($IMG): installing $pkgbase, then serving the packaged desktop"
echo "▸ → http://localhost:${PORT}/   (-no-auth, apps-dir /usr/bin; Ctrl-C to stop)"
docker run --rm --name "$NAME" -p "${PORT}:11000" -v "$(dirname "$pkgfile")":/pkg:ro "$IMG" \
  sh -c "$INST >/dev/null 2>&1 && echo '── wash $pkgbase serving on :11000 ──' && exec /usr/bin/wash-router -http -no-auth -listen 0.0.0.0:11000 -apps-dir /usr/bin"
