#!/bin/sh
# Builds the jail image (wash-jail) from this checkout's multicall binary and
# the fake ACP adapter, then runs its smoke test:
#
#   make multicall out/e2e/codex-acp
#   tools/openrouter-eval/jail/build.sh [--no-smoke]
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/../../.." && pwd)
ctx=$(mktemp -d)
trap 'rm -rf "$ctx"' EXIT
cp "$repo/out/wash" "$repo/out/e2e/codex-acp" "$here/Dockerfile" "$here/smoke.sh" "$here/run.sh" "$here/../audit.py" "$here/prewarm.mjs" "$ctx/"
cp -a "$repo/e2e/shakedown" "$ctx/shakedown"
mkdir -p "$ctx/team" && cp -a "$here/../bench/team/project" "$here/../bench/team/hidden" "$ctx/team/"
docker build -q -t wash-jail "$ctx" >/dev/null || { docker build -t wash-jail "$ctx"; exit 1; }
echo "built wash-jail"
[ "${1:-}" = --no-smoke ] && exit 0
docker run --rm wash-jail sh /opt/jail/smoke.sh
