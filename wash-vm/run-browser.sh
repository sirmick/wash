#!/usr/bin/env bash
# run-browser.sh — start the in-browser (TinyEMU RISC-V) wash-vm dev server on
# :13000. The page boots the WASM VM in the tab; the wash desktop + login are
# served BY the VM over the virtio-console channel (wash-vm/UNIFY.md), the dev
# server only hosts index.html + the bridge + the VM artifacts.
#
#   wash-vm/run-browser.sh            # → http://localhost:13000
#   PORT=9000 wash-vm/run-browser.sh  # override the port
#
# Companion to run-qemu.sh, which serves the same wash UI from a real QEMU VM on
# the same port (run one or the other).
set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

PORT="${PORT:-13000}"
export PORT

# The host page imports the shell bundle from /shell/shell.js; build it first.
pnpm -F @wash/shell build

# The WASM VM needs the RISC-V kernel + firmware + rootfs under
# wash-vm/web/public/tinyemu/ (built by `make -C wash-vm/image all`). Warn rather
# than fail — the dev server still runs (e.g. for front-end-only iteration).
if [[ ! -f wash-vm/web/public/tinyemu/wash-kernel.bin ]]; then
  echo "run-browser.sh: WARNING — RISC-V VM image missing; run 'make -C wash-vm/image all' to boot the VM." >&2
fi

echo "run-browser.sh: in-browser wash-vm dev server on http://localhost:${PORT}"
exec node wash-vm/web/server/server.mjs
