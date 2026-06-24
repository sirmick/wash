#!/usr/bin/env bash
# run.sh — start the wash-router from out/ in --dev mode.
#
# Modes:
#   --multicall (default)  — exec out/wash-router (a symlink to out/wash).
#                            `make wash` assembles that layout directly in out/.
#                            This is the SHIPPED layout, so dev defaults to it.
#   --standalone           — exec out/singlecall/wash-router (the per-app layout,
#                            built by `make wash-standalone`).
#
# Other flags:
#   --no-build       — skip the build.sh step
#   --listen HOST:PORT — override default 0.0.0.0:11000
#   --fm-seed [PATH] — seed /tmp/wash-fm-smoke (or PATH) and pass
#                      --fs-root to the router
#   --tail           — exec in foreground tee'd to /tmp/wash-router.log
#   --no-session     — pass --no-session (kiosk-ish)
#
# Always rebuilds before starting unless --no-build is set. The router
# runs with --dev so editing source + re-running `build.sh` triggers
# an automatic respawn of the affected app (and a shell reload for FE
# changes).
#
# Existing wash processes are killed first (via scripts/dev-kill.sh)
# so the rebuilt binaries take over cleanly — no stale router.

set -euo pipefail

REPO="$(cd "$(dirname "$0")" && pwd)"
cd "$REPO"

# Admin tools netd shells to (netplan, ip, ifup) live in /usr/sbin, which a
# desktop login's PATH often omits. The dev router runs netd as the user (a
# real deploy runs it as root, where sbin is present), so add sbin here — else
# netplan/ifupdown autodetect can't find their binaries. (docs/NET-BACKENDS.md)
export PATH="/usr/sbin:/sbin:$PATH"

mode=multicall
do_build=1
listen="0.0.0.0:11000"
fm_root=""
fm_seed=0
tail_log=0
no_session=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --standalone) mode=standalone; shift;;
    --multicall|--bb|--busybox) mode=multicall; shift;;
    --no-build) do_build=0; shift;;
    --listen)   listen="$2"; shift 2;;
    --no-session) no_session=1; shift;;
    --tail)     tail_log=1; shift;;
    --fm-seed)
      fm_seed=1
      if [[ $# -ge 2 && "${2:-}" != --* ]]; then
        fm_root="$2"; shift 2
      else
        fm_root="${fm_root:-/tmp/wash-fm-smoke}"; shift
      fi
      ;;
    -h|--help)
      sed -n '1,/^set -euo/p' "$0" | sed 's/^# \{0,1\}//' | sed '$d'
      exit 0
      ;;
    *)
      echo "run.sh: unknown flag: $1" >&2
      exit 2
      ;;
  esac
done

if [[ "$do_build" == "1" ]]; then
  case "$mode" in
    standalone) make -C "$REPO" wash-standalone;;
    *)          make -C "$REPO" wash;;
  esac
fi

# Kill any stale wash processes so the dev-reload watcher in the new
# router doesn't fight a previous one for /tmp/wash-<uid>.sock etc.
"$REPO/scripts/dev-kill.sh"

# Optional fm sandbox.
if [[ "$fm_seed" == "1" ]]; then
  "$REPO/scripts/fm-seed.sh" "$fm_root"
fi

# Default (multicall) mode: the runnable image lives directly in out/ (wash +
# wash-* symlinks), assembled by `make wash`. Pick the router from there;
# --standalone mode runs the per-app binary in out/singlecall/ (`make wash-standalone`).
router_bin="$REPO/out/wash-router"
build_hint="make wash"
if [[ "$mode" == "standalone" ]]; then
  router_bin="$REPO/out/singlecall/wash-router"
  build_hint="make wash-standalone"
fi
if [[ ! -e "$router_bin" ]]; then
  echo "run.sh: $router_bin missing — run '$build_hint' first" >&2
  exit 1
fi

args=(
  --listen "$listen"
  --dev
  --show-hidden
  --screenshot-dir "${WASH_SCREENSHOT_DIR:-/tmp/wash-screenshots}"
)
[[ "$no_session" == "1" ]] && args+=(--no-session)
[[ -n "$fm_root"      ]] && args+=(--fs-root "$fm_root")

mkdir -p "${WASH_SCREENSHOT_DIR:-/tmp/wash-screenshots}"
LOG=/tmp/wash-router.log
: > "$LOG"

if [[ "$tail_log" == "1" ]]; then
  echo "run.sh: foreground tail of $LOG"
  exec "$router_bin" "${args[@]}" 2>&1 | tee -a "$LOG"
fi

nohup "$router_bin" "${args[@]}" >>"$LOG" 2>&1 &
router_pid=$!

# Wait for "listening on" before returning so the caller can hit the
# URL immediately. Bounded by 5s — well past actual startup time on
# this hardware; if we time out, something is broken in the binary.
for _ in $(seq 1 50); do
  if grep -q "listening on" "$LOG" 2>/dev/null; then
    echo "run.sh: router up (pid=$router_pid) — http://$listen/  [$mode]"
    tail -3 "$LOG"
    exit 0
  fi
  if ! kill -0 "$router_pid" 2>/dev/null; then
    echo "run.sh: router exited before 'listening on'" >&2
    tail -20 "$LOG" >&2
    exit 1
  fi
  sleep 0.1
done

echo "run.sh: timed out waiting for 'listening on'" >&2
tail -20 "$LOG" >&2
exit 1
