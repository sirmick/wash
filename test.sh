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

while [[ $# -gt 0 ]]; do
  case "$1" in
    --standalone) mode=standalone; shift;;
    --multicall|--bb|--busybox) mode=multicall; shift;;
    --both)       mode=both; shift;;
    --no-unit)    do_unit=0; shift;;
    --no-e2e)     do_e2e=0; shift;;
    --no-build)   do_build=0; shift;;
    --filter)     filter="$2"; shift 2;;
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
  # -p 1 forces one test package at a time. internal/loopback's QoS
  # soak test makes strict latency assertions on the scheduler;
  # parallel package execution that also spins up routers
  # (internal/router, internal/sdk) loads the runtime enough that
  # the soak's bulk-flood goroutines starve and the package hits
  # the 10-minute timeout. Serial keeps the bounds honest.
  if go test -count=1 -p 1 "$@" ./...; then
    echo "test.sh: unit ($label) PASS"
  else
    echo "test.sh: unit ($label) FAIL" >&2
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
  if env "$@" pnpm --dir "$REPO/e2e" exec playwright test "${extra[@]}" --reporter=line --workers=1; then
    echo "test.sh: e2e ($label) PASS"
  else
    echo "test.sh: e2e ($label) FAIL" >&2
    return 1
  fi
}

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
