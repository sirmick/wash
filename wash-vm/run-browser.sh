#!/usr/bin/env bash
# run-browser.sh — start the in-browser (TinyEMU RISC-V) wash-vm dev server on
# :12000. The page boots the WASM VM in the tab; the wash desktop + login are
# served BY the VM over the virtio-console channel (wash-vm/UNIFY.md), the dev
# server only hosts index.html + the bridge + the VM artifacts.
#
#   wash-vm/run-browser.sh            # → http://localhost:12000
#   PORT=9000 wash-vm/run-browser.sh  # override the port
#
# Companion to run-qemu.sh, which serves the same wash UI from a real QEMU VM on
# the same port (run one or the other).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PORT="${PORT:-12000}"
HOST="${HOST:-0.0.0.0}"   # 0.0.0.0 = reachable from other devices on the LAN
export PORT HOST

# The host page imports the shell bundle from /shell/shell.js; build it first.
pnpm -F @wash/shell build

# wash-vm/web (@wash/demo) is a standalone package, not a pnpm workspace member,
# so the root install doesn't cover its server deps (ws, vite). Install them
# into wash-vm/web/node_modules on first run (fast once the store is warm).
if [[ ! -d wash-vm/web/node_modules ]]; then
  echo "run-browser.sh: installing wash-vm/web deps (ws, vite)…"
  (cd wash-vm/web && pnpm install --ignore-workspace --prefer-offline)
fi

# The WASM VM needs the RISC-V kernel + firmware + rootfs under
# wash-vm/web/public/tinyemu/ (built by `make -C wash-vm/image all`). Warn rather
# than fail — the dev server still runs (e.g. for front-end-only iteration).
if [[ ! -f wash-vm/web/public/tinyemu/wash-kernel.bin ]]; then
  echo "run-browser.sh: WARNING — RISC-V VM image missing; run 'make -C wash-vm/image all' to boot the VM." >&2
fi

echo "run-browser.sh: in-browser wash-vm dev server on http://localhost:${PORT}"
exec node wash-vm/web/server/server.mjs
