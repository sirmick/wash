#!/usr/bin/env bash
# dev-restart — full bootstrap-or-restart of the live wash dev
# router on 0.0.0.0:11000. Encodes the kill-wait-cp-restart sequence
# captured in CLAUDE memory note "wash-dev-loop": child apps hold
# binaries open after the router exits, so `cp out/* /tmp/...`
# requires a hard kill + sleep first.
#
# Sequence:
#   1. make TEST_APP=1 all      (unless --no-build)
#   2. dev-kill.sh
#   3. cp $REPO/out/* /tmp/wash-dev-apps/
#   4. (optionally) fm-seed.sh into the sandbox root
#   5. spawn wash-router in background, tee-ing /tmp/wash-router.log
#   6. wait for "listening on" line, then return
#
# Flags:
#   --no-build              skip make
#   --fm-root PATH          export WASH_FM_ROOT=PATH for the router
#                           (and its children — fm reads it on start)
#   --fm-seed [PATH]        also run fm-seed.sh; implies --fm-root.
#                           PATH defaults to /tmp/wash-fm-smoke.
#   --no-session            pass --no-session to the router (kiosk-ish)
#   --tail                  exec foreground with `| tee`; Ctrl-C kills
#   --port PORT             override 11000
#   --listen HOST:PORT      override 0.0.0.0:11000 entirely

set -euo pipefail

REPO="$(cd "$(dirname "$0")/.." && pwd)"
APPS_DIR="${WASH_DEV_APPS:-/tmp/wash-dev-apps}"
SCREENSHOT_DIR="${WASH_SCREENSHOT_DIR:-/tmp/wash-screenshots}"
LOG=/tmp/wash-router.log

build=1
fm_root=""
fm_seed=0
no_session=0
tail_log=0
listen="0.0.0.0:11000"
bulk_seed=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-build)    build=0; shift;;
    --fm-root)     fm_root="$2"; shift 2;;
    --fm-seed)
      fm_seed=1
      # Optional PATH after --fm-seed. Treat next arg as path only
      # if it doesn't start with "--".
      if [[ $# -ge 2 && "${2:-}" != --* ]]; then
        fm_root="$2"; shift 2
      else
        fm_root="${fm_root:-/tmp/wash-fm-smoke}"; shift
      fi
      ;;
    --no-session)  no_session=1; shift;;
    --tail)        tail_log=1; shift;;
    --bulk-seed)
      bulk_seed=1
      if [[ $# -ge 2 && "${2:-}" != --* ]]; then
        fm_root="$2"; shift 2
      else
        fm_root="${fm_root:-/tmp/wash-fm-smoke}"; shift
      fi
      ;;
    --port)        listen="0.0.0.0:$2"; shift 2;;
    --listen)      listen="$2"; shift 2;;
    -h|--help)
      sed -n '1,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
      exit 0
      ;;
    *)
      echo "dev-restart: unknown flag: $1" >&2
      echo "(use --help to list flags)" >&2
      exit 2
      ;;
  esac
done

# 1. Build.
if [[ "$build" == "1" ]]; then
  (cd "$REPO" && make TEST_APP=1 all)
fi

# 2. Kill any running wash processes.
"$REPO/scripts/dev-kill.sh"

# 3. Stage the freshly-built binaries.
mkdir -p "$APPS_DIR" "$SCREENSHOT_DIR"
cp "$REPO/out/"* "$APPS_DIR/"

# 4. Optional sandbox seed.
if [[ "$fm_seed" == "1" ]]; then
  "$REPO/scripts/fm-seed.sh" "$fm_root"
fi
if [[ "$bulk_seed" == "1" ]]; then
  "$REPO/scripts/seed-bulk-fixture.sh" "$fm_root"
fi

# 5. Launch.
args=(
  --listen "$listen"
  --apps-dir "$APPS_DIR"
  --show-hidden
  --screenshot-dir "$SCREENSHOT_DIR"
)
if [[ "$no_session" == "1" ]]; then
  args+=(--no-session)
fi

env_kv=()
if [[ -n "$fm_root" ]]; then
  env_kv+=("WASH_FM_ROOT=$fm_root")
fi
# Pass through WASH_BULKOPS_ITEM_DELAY_MS if the caller exported it
# in their shell — handy for ad-hoc UI testing (see wash-bulk's
# main.go). No script flag for this; it's a one-off knob.
if [[ -n "${WASH_BULKOPS_ITEM_DELAY_MS:-}" ]]; then
  env_kv+=("WASH_BULKOPS_ITEM_DELAY_MS=$WASH_BULKOPS_ITEM_DELAY_MS")
fi

: > "$LOG"

if [[ "$tail_log" == "1" ]]; then
  echo "dev-restart: foreground tail of $LOG"
  exec env "${env_kv[@]}" "$REPO/out/wash-router" "${args[@]}" 2>&1 | tee -a "$LOG"
fi

# Background launch — wait for "listening on" before returning so
# the caller can immediately start using the router.
nohup env "${env_kv[@]}" "$REPO/out/wash-router" "${args[@]}" >>"$LOG" 2>&1 &
router_pid=$!

for _ in $(seq 1 50); do
  if grep -q "listening on" "$LOG" 2>/dev/null; then
    echo "dev-restart: router up (pid=$router_pid) — http://$listen/"
    tail -3 "$LOG"
    exit 0
  fi
  if ! kill -0 "$router_pid" 2>/dev/null; then
    echo "dev-restart: router exited before 'listening on'" >&2
    tail -20 "$LOG" >&2
    exit 1
  fi
  sleep 0.1
done

echo "dev-restart: timed out waiting for 'listening on'" >&2
tail -20 "$LOG" >&2
exit 1
