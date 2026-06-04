#!/usr/bin/env bash
# build-display — build wash-display (the native X/Wayland compositor BE)
# and drop the binary into out/ alongside the other wash bins.
#
# wash-display is C++/CMake, not Go, and is opt-in from the normal build
# (WASH_DISPLAY=1). This wraps the Makefile's out/wash-display target so the
# build recipe stays in one place: it builds the settings-panel FE first
# (web-display, embedded as raw bytes at CMake configure time), then
# configures + builds the CMake project and copies the binary into out/.
#
# Usage:
#   scripts/build-display.sh            # build out/wash-display
#   scripts/build-display.sh --clean    # wipe the CMake build dir first
#
# Requires cmake plus the native deps (wlroots, libdatachannel) on the
# system — see docs/DISPLAY.md §8 and wash-display/CMakeLists.txt.

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO"

clean=0
for arg in "$@"; do
  case "$arg" in
    --clean) clean=1;;
    -h|--help)
      sed -n '2,18p' "$0"; exit 0;;
    *)
      echo "build-display: unknown argument: $arg" >&2
      echo "try: $0 --help" >&2
      exit 2;;
  esac
done

if ! command -v cmake >/dev/null 2>&1; then
  echo "build-display: cmake not found — install it (and wlroots/libdatachannel)." >&2
  echo "  see docs/DISPLAY.md §8" >&2
  exit 1
fi

if [[ $clean -eq 1 ]]; then
  echo "build-display: removing wash-display/build"
  rm -rf wash-display/build
fi

echo "build-display: building out/wash-display"
make out/wash-display

echo "build-display: done -> $REPO/out/wash-display"
