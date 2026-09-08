#!/usr/bin/env bash
# build.sh — build wash.
#
# A thin front door onto the Makefile, which is the real build: `make`
# assembles the FE bundles, embeds them, and links the multicall binary
# plus its wash-<app> symlinks into out/. Shell scripts were deliberately
# retired in favour of that facade, so this stays a wrapper rather than a
# second implementation that can drift from it.
#
#   ./build.sh              # everything (out/wash + symlinks + wash-login)
#   ./build.sh test         # build, then the unit tier
#   ./build.sh e2e          # build, then the full browser suite
#   ./build.sh <make-target># anything the Makefile defines
#
# See `make help` for the full target list.
set -euo pipefail
cd "$(dirname "$0")"

case "${1:-}" in
  "")     exec make ;;
  test)   exec make unit-test ;;
  e2e)    exec make e2e-test ;;
  *)      exec make "$@" ;;
esac
