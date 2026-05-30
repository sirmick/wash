#!/usr/bin/env bash
# test.sh — run wash's unit + e2e suites.
#
# Modes (mirror build.sh):
#   --standalone (default) — exercise the per-app binary layout
#   --multicall            — exercise the multi-call + symlinks layout
#                            (sets WASH_E2E_MULTICALL=1 + builds tags)
#   --both                 — run the full sequence in both layouts
#
# Other flags:
#   --no-unit              — skip go tests
#   --no-e2e               — skip playwright suite
#   --filter <pattern>     — passed to `playwright test <pattern>`
#                            (run only matching specs)
#   --workers <N>          — playwright workers. Default: nproc/2.
#                            Each worker spawns its own router +
#                            apps; tune down on memory-constrained
#                            boxes or when chasing inotify-instance
#                            limits.
#   --no-build             — assume out/ is current; skip build.sh
#
# Exit non-zero on the first failing suite. Test output streams
# directly to stdout/stderr — redirect to a file yourself if you
# want a captured log:
#   ./test.sh --both 2>&1 | tee /tmp/test.log

set -euo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO"

mode=standalone
do_unit=1
do_e2e=1
do_build=1
filter=""
# Playwright workers. The fixture allocates a unique port + tmpdir
# per test, so >1 is safe in principle; default to half the CPU
# cores so a test machine doesn't saturate. Override with --workers.
e2e_workers=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --standalone) mode=standalone; shift;;
    --multicall|--bb|--busybox) mode=multicall; shift;;
    --both)       mode=both; shift;;
    --no-unit)    do_unit=0; shift;;
    --no-e2e)     do_e2e=0; shift;;
    --no-build)   do_build=0; shift;;
    --filter)     filter="$2"; shift 2;;
    --workers)    e2e_workers="$2"; shift 2;;
    -h|--help)
      sed -n '1,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
      exit 0
      ;;
    *)
      echo "test.sh: unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

# Builds first (unless --no-build). `--both` runs both layouts'
# unit + e2e, so the build step must produce both kinds of output.
if [[ "$do_build" == "1" ]]; then
  case "$mode" in
    both)       "$REPO/build.sh" --both;;
    multicall)  "$REPO/build.sh" --multicall;;
    *)          "$REPO/build.sh" --standalone;;
  esac
fi

# run_unit and run_e2e are layout-tagged so the --both path can call
# each twice with different settings. They redirect to /tmp logs so a
# downstream `tail` survives the script exit.

run_unit() {
  local label="$1"; shift
  echo
  echo "════ test.sh: unit ($label) ════"
  # -p 1 forces one test package at a time so the loopback package
  # (router+sdk wire end-to-end via in-memory pipes) doesn't race
  # other in-process tests for goroutine scheduling.
  if go test -count=1 -p 1 -timeout 60s "$@" ./...; then
    echo "test.sh: unit ($label) PASS"
  else
    echo "test.sh: unit ($label) FAIL" >&2
    return 1
  fi
}

run_fe_unit() {
  local label="$1"
  echo
  echo "════ test.sh: fe-unit ($label) ════"
  # FE unit tests: Node's built-in runner over *.test.ts, using Node
  # 22+'s native TypeScript type-stripping — no tsx/vitest/build step,
  # matching the web/shell/*.test.ts precedent. Covers framework-free
  # logic under web/** and apps/**/fe/**; anything needing a real DOM
  # stays in the Playwright e2e suite. Layout-independent (no build
  # tags), so it runs once regardless of standalone/multicall mode.
  local files
  files=$(find "$REPO/web" "$REPO/apps" -path '*/node_modules' -prune -o \
    -name '*.test.ts' -not -path '*/dist/*' -print 2>/dev/null)
  if [[ -z "$files" ]]; then
    echo "test.sh: fe-unit ($label) — no test files, skipping"
    return 0
  fi
  # shellcheck disable=SC2086
  if node --test $files; then
    echo "test.sh: fe-unit ($label) PASS"
  else
    echo "test.sh: fe-unit ($label) FAIL" >&2
    return 1
  fi
}

run_e2e() {
  local label="$1"; shift
  echo
  echo "════ test.sh: e2e ($label) ════"
  rm -rf "$REPO/e2e/test-results"/*
  local extra=()
  [[ -n "$filter" ]] && extra+=("$filter")
  # Resolve workers at run time. 0 → half of nproc (rounded up),
  # capped at 8 so a 32-core machine doesn't try to keep 32 routers
  # alive concurrently — each one spawns ~5 BE apps + a chromium
  # tab, and the inotify-instances default ceiling is 128 per user.
  local workers="$e2e_workers"
  if [[ "$workers" == "0" ]]; then
    if command -v nproc >/dev/null 2>&1; then
      workers=$(( ( $(nproc) + 1 ) / 2 ))
    else
      workers=2
    fi
    (( workers > 8 )) && workers=8
    (( workers < 1 )) && workers=1
  fi
  echo "test.sh: e2e workers=$workers"
  if env "$@" pnpm --dir "$REPO/e2e" exec playwright test "${extra[@]}" --reporter=line --workers="$workers"; then
    echo "test.sh: e2e ($label) PASS"
  else
    echo "test.sh: e2e ($label) FAIL" >&2
    return 1
  fi
}

# FE unit tests are layout-independent — run them once up front when
# unit tests are enabled, before the per-mode go/e2e sequence.
[[ "$do_unit" == "1" ]] && run_fe_unit node

# Run sequence per mode.
case "$mode" in
  standalone)
    [[ "$do_unit" == "1" ]] && run_unit standalone
    [[ "$do_e2e"  == "1" ]] && run_e2e standalone
    ;;
  multicall)
    [[ "$do_unit" == "1" ]] && run_unit multicall -tags=multicall
    [[ "$do_e2e"  == "1" ]] && run_e2e multicall WASH_E2E_MULTICALL=1
    ;;
  both)
    [[ "$do_unit" == "1" ]] && run_unit standalone
    [[ "$do_unit" == "1" ]] && run_unit multicall -tags=multicall
    [[ "$do_e2e"  == "1" ]] && run_e2e standalone
    [[ "$do_e2e"  == "1" ]] && run_e2e multicall WASH_E2E_MULTICALL=1
    ;;
esac

echo "test.sh: all suites passed"
