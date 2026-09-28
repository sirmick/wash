#!/bin/sh
# Runs the live shakedown (e2e/shakedown, driven by
# e2e/tests/agent-shakedown-live.spec.ts) with one mix from mixes/, and keeps
# everything it leaves under results/: the scorecard, check.sh's output, the
# orchestrator's transcript, the workspace state, the plan and QA files and
# the git log.
#
#   tools/openrouter-eval/team.sh glm53-glm53flash [minutes]
#
# Needs the multicall test layout built: make test-app && make TEST_APP=1 multicall.
# Spends real money on the stored OpenRouter key.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../.." && pwd)
mix=$1
minutes=${2:-40}
[ -f "$here/mixes/$mix.json" ] || { echo "no mix $here/mixes/$mix.json" >&2; exit 2; }
out="$here/results/$(date +%F-%H%M)-team-$mix"
cd "$repo/e2e"
status=0
WASH_E2E_LIVE=1 WASH_E2E_MULTICALL=1 WASH_E2E_SKIP_VM=1 \
  WASH_E2E_CATALOG="$here/mixes/$mix.json" WASH_E2E_MINUTES="$minutes" \
  pnpm exec playwright test tests/agent-shakedown-live.spec.ts --output "$out" --trace off 2>&1 | tee "$here/.last-run.log" || status=$?
status=${status:-0}
grep -q '1 passed' "$here/.last-run.log" || status=1
# Playwright nests outputs in a directory named after the test; flatten it.
for d in "$out"/*/; do
  [ -d "$d" ] && cp -a "$d". "$out"/ && rm -rf "$d"
done
mv "$here/.last-run.log" "$out/run.log"
# What every session actually did, from OpenCode's own record.
project=$(sed -n 's/^shakedown project: //p' "$out/run.log" | head -1)
[ -n "$project" ] && "$here/audit.py" "$project" > "$out/audit.txt" && tail -1 "$out/audit.txt"
echo "results: $out (exit $status)"
[ -f "$out/scorecard.json" ] && cat "$out/scorecard.json"
exit $status
