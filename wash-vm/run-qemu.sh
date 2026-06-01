#!/usr/bin/env bash
# run-qemu.sh — boot the wash-vm QEMU microvm ("wemu") and front it with the
# proxy on :13000. The wash desktop + login are served BY the VM over the
# virtio-serial channel (wash-vm/UNIFY.md); the proxy only hosts the minimal
# chrome + tunnels the wire. Log in with wash / wash.
#
#   wash-vm/run-qemu.sh                 # → http://localhost:13000
#   wash-vm/run-qemu.sh -smp 2 -m 2048  # extra args pass straight through to qemu
#   ADDR=0.0.0.0:13000 wash-vm/run-qemu.sh   # override the bind address
#
# Anything on the command line is forwarded verbatim to qemu (appended after
# wash's defaults, so it overrides/adds — docs/NET.md §8.2). Companion to
# run-browser.sh, which serves the same UI from the in-browser VM on the same
# port (run one or the other).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

ADDR="${ADDR:-0.0.0.0:13000}"   # 0.0.0.0 = reachable from other devices on the LAN

# Build the image + minimal chrome + host runner (incremental — skipped when
# already current; the Alpine render is docker-cached).
make vm-image vm-chrome out/washvm-run

echo "run-qemu.sh: booting wemu, proxy on http://${ADDR}  (login: wash / wash)"
exec out/washvm-run --chrome out/vm-chrome --addr "$ADDR" -- "$@"
