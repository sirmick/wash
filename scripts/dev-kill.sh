#!/usr/bin/env bash
# dev-kill — terminate every wash process this user is running.
#
# Catches:
#   - the live router at /home/mick/wash/out/wash-router
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

pkill -9 -f 'out/wash-router'                      2>/dev/null || true
pkill -9 -f 'wash-(session|fm|term|about|test)'    2>/dev/null || true
pkill -9 -f 'wash-e2e-apps'                        2>/dev/null || true
pkill -9 -f 'tee.*wash-router\.log'                2>/dev/null || true

sleep 2

remaining=$(pgrep -af 'wash-(router|session|fm|term|about|test)' || true)
if [[ -n "$remaining" ]]; then
  echo "dev-kill: still running after pkill -9 + sleep:" >&2
  echo "$remaining" >&2
  exit 1
fi
echo "dev-kill: all wash processes terminated"
