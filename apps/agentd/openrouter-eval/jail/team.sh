#!/bin/sh
# One shakedown run of a mix (../mixes/<mix>.json) in the jail. Nothing of
# the host is mounted but a new output folder under ../results; the
# OpenRouter key goes in through the environment, never the command line.
#
#   sg docker -c 'apps/agentd/openrouter-eval/jail/team.sh glm53-glm53flash [minutes] [shakedown|team]'
set -eu
here=$(cd "$(dirname "$0")" && pwd)
mix=$1
minutes=${2:-45}
project=${3:-shakedown}
[ -f "$here/../mixes/$mix.json" ] || { echo "no mix $mix" >&2; exit 2; }
out="$here/../results/$(date +%F-%H%M)-jail-$project-$mix"
mkdir -p "$out"
CATALOG=$(jq -c . "$here/../mixes/$mix.json") \
OPENROUTER_API_KEY=$(jq -r .openrouter "${XDG_CONFIG_HOME:-$HOME/.config}/wash/keys.json") \
MINUTES=$minutes PROJECT=$project \
  docker run --rm --name "wash-jail-$project-$mix-$$" -e CATALOG -e OPENROUTER_API_KEY -e MINUTES -e PROJECT \
    -v "$out:/out:Z" wash-jail sh /opt/jail/run.sh >"$out/run.log" 2>&1 || true
echo "results: $out"
cat "$out/scorecard.json" 2>/dev/null || tail -20 "$out/run.log"
