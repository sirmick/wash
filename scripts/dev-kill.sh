#!/usr/bin/env bash
# dev-kill — terminate every wash process this user is running.
#
# Catches:
#   - the live router (out/multicall/wash-router by default, or the
#     standalone out/wash-router)
#   - its app children (session, fm, about, term, test) wherever
#     they were execed from
#   - leftover binaries from e2e runs (paths under /tmp/wash-e2e-apps-*)
#   - the `tee` wrapper around /tmp/wash-router.log
#
# Children hold their binaries open after the router exits, so any
# `cp out/wash-* /tmp/wash-dev-apps/` that follows would hit "Text
# file busy" without this kill+wait. After the pkill volley we sleep
# briefly to let the kernel release fd locks, then verify nothing
# survived — exit 1 if it did.

set -euo pipefail

# Sweep three buckets:
#   1. The live dev router and its app children (matched by the
#      /tmp/wash-dev-apps/ prefix and the wash-router pathname).
#   2. Per-test e2e processes whose paths contain wash-e2e-apps-* —
#      these accumulate alarmingly if a test run is interrupted
#      (each adds an inotify instance; the default per-user limit
#      is ~128, after which fsnotify watchers silently fail).
#   3. The `tee` wrapper around /tmp/wash-router.log.
#
# Use -f to match by full command line — wash binaries are named
# after their app role, so a bare process-name pkill misses
# e2e-spawned copies whose paths differ from the dev tree.

pkill -9 -f 'out/(multicall/)?wash-router'         2>/dev/null || true
pkill -9 -f 'wash-(session|fm|term|about|test|bulk)'    2>/dev/null || true
pkill -9 -f 'wash-e2e-apps'                        2>/dev/null || true
pkill -9 -f 'tee.*wash-router\.log'                2>/dev/null || true

sleep 2

# Final verification: any wash- binary running for this user must
# now be gone. If something survived, surface it loudly — the
# accumulation pattern silently breaks the next test run.
#
# The regex MUST require the binary name to be followed by a path
# separator boundary (whitespace, end-of-line, or a flag), not a
# dot — otherwise the bash command line of THIS script itself
# matches (it references "/tmp/wash-router.log"), and dev-restart
# self-aborts thinking its parent shell is a runaway wash process.
remaining=$(pgrep -af 'wash-(router|session|fm|term|about|test)(\s|$|\b/)' || true)
if [[ -n "$remaining" ]]; then
  echo "dev-kill: still running after pkill -9 + sleep:" >&2
  echo "$remaining" >&2
  exit 1
fi

# Sanity check on per-user inotify pressure. The Linux default is
# 128 instances; each running fm Manager holds one. We print a
# warning at >50% so a slow leak gets caught before the limit hits.
# The `find` walks /proc/*/fd and trips on root-owned dirs we
# can't read — that errors under `set -o pipefail`, so swallow with
# `|| true` and let wc count whatever we did get.
if [[ -r /proc/sys/fs/inotify/max_user_instances ]]; then
  limit=$(cat /proc/sys/fs/inotify/max_user_instances)
  used=$( (find /proc/*/fd -lname 'anon_inode:inotify' 2>/dev/null || true) | wc -l)
  if (( used * 2 > limit )); then
    echo "dev-kill: warning: $used/$limit inotify instances in use" >&2
  fi
fi
echo "dev-kill: all wash processes terminated"
