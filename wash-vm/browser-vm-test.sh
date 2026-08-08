#!/usr/bin/env bash
# browser-vm-test.sh — headless boot gate for the in-browser (TinyEMU RISC-V)
# wash-vm. Starts the dev server, drives it with a Playwright smoke, and asserts
# the wash desktop mounts. Self-skips (exit 0) when the RISC-V image isn't built
# — mirroring how the KVM e2e specs skip without /dev/kvm — so a plain checkout
# doesn't hard-fail. Build the image first with `make browser-image-vm`.
set -eu

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

KERNEL=wash-vm/web/public/tinyemu/wash-kernel.bin
if [ ! -f "$KERNEL" ]; then
  echo "browser-vm-test: SKIP — $KERNEL missing (run 'make browser-image-vm' first)"
  exit 0
fi
if [ ! -d e2e/node_modules/@playwright ]; then
  echo "browser-vm-test: installing e2e Playwright deps…"
  (cd e2e && pnpm install --ignore-workspace --silent && pnpm exec playwright install chromium)
fi

PORT="${PORT:-12070}"
PORT="$PORT" HOST=127.0.0.1 wash-vm/run-browser.sh > /tmp/wash-browser-vm-test-server.log 2>&1 &
SRV=$!
cleanup() { kill "$SRV" 2>/dev/null || true; wait "$SRV" 2>/dev/null || true; }
trap cleanup EXIT

for _ in $(seq 1 90); do
  curl -sf "http://127.0.0.1:$PORT/" >/dev/null 2>&1 && break
  sleep 1
done

SMOKE_URL="http://127.0.0.1:$PORT" node wash-vm/browser-vm-smoke.mjs
