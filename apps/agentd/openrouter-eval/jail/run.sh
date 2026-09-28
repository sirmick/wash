#!/bin/sh
# One shakedown run inside the jail, the same run as
# e2e/tests/agent-shakedown-live.spec.ts but with no browser: wash headless,
# the Agent app driven over the control socket (launch, msg, watch), and this
# script as the owner the shakedown's SCRIPT.md describes. It answers the
# gamma question (step 7) as the script says, leaves "Ready to end?" alone,
# and, after three quiet minutes with the orchestrator idle, says to carry
# on (counted). Everything is written to /out.
#
# Environment: CATALOG (the mix's catalog JSON), OPENROUTER_API_KEY, MINUTES.
set -eu
out=/out
minutes=${MINUTES:-45}
work=/home/agent/project
state=/home/agent/.local/state/wash
sock=/tmp/wash-$(id -u).sock
mkdir -p "$out" /home/agent/.config/wash

# The mix as the workspace catalog, and the key where wash reads it.
printf '{"catalogs":{"shakedown":%s}}\n' "$CATALOG" >/home/agent/.config/wash/agents.json
jq -n --arg k "$OPENROUTER_API_KEY" '{openrouter:$k}' >/home/agent/.config/wash/keys.json
chmod 600 /home/agent/.config/wash/keys.json

cp -R /opt/jail/shakedown "$work"
cd "$work"
git init -q && git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -qm "shakedown: start"

wash-router -http -no-auth -no-session -listen 127.0.0.1:11000 -apps-dir /opt/wash >"$out/router.log" 2>&1 &
for _ in $(seq 50); do [ -S "$sock" ] && break; sleep 0.2; done
ctl() { printf '%s\n' "$1" | socat -t 10 - "UNIX-CONNECT:$sock"; }
inst=$(ctl '{"t":"launch","app_id":"com.wash.ai"}' | jq -r '.instance_id')
msg() { ctl "{\"t\":\"msg\",\"instance_id\":\"$inst\",\"data\":$1}" >/dev/null; }
(printf '{"t":"watch","instance_id":"%s"}\n' "$inst"; sleep 100000) | socat - "UNIX-CONNECT:$sock" >"$out/watch.jsonl" &

began=$(date +%s)
prompt='Read SCRIPT.md and run the shakedown. Commit with git -c user.name=shakedown -c user.email=shakedown@localhost.'
msg "$(jq -nc --arg p "$prompt" --arg cwd "$work" '{kind:"agent_start",catalog:"shakedown",model:"frontier",cwd:$cwd,prompt:$p,yolo:true,claim:true}')"
key=""
for _ in $(seq 150); do
  key=$(jq -r 'select(.t=="msg" and .data.kind=="agent_started") | .data.key // empty' "$out/watch.jsonl" 2>/dev/null | head -1)
  [ -n "$key" ] && break
  sleep 0.2
done
[ -n "$key" ] || { echo "the session did not start"; exit 1; }
session=$(jq -r 'select(.t=="msg" and .data.kind=="agent_started") | .data.session_id' "$out/watch.jsonl" | head -1)

# Latest event time across every session's transcript, in ms.
last_event() { cat "$state"/agent-transcripts/*.jsonl 2>/dev/null | jq -s 'map(.at_ms // 0) | max // 0'; }
# Whether the orchestrator's turn is running, from the latest roster push.
working() { jq -r --arg k "$key" 'select(.t=="msg") | .data | select(.kind=="session_state" or .kind=="state") | .state.rows[]? | select(.key==$k) | .state' "$out/watch.jsonl" | tail -1 | grep -qx working; }

answered=0 prods=0 timed_out=false
while :; do
  now=$(date +%s)
  if [ $((now - began)) -gt $((minutes * 60)) ]; then timed_out=true; break; fi
  msg "{\"kind\":\"transcript_subscribe\",\"key\":\"$key\"}"
  # The owner, step 7: the gamma question gets gamma, with a note.
  if [ "$answered" = 0 ]; then
    q=$(jq -r '[.workspaces[] | select(.state != "ended") | .messages[]? | select(.type=="decision_request" and .delivery=="recorded" and (.body | test("gamma.txt")))][0].id // empty' "$state/workspaces.json" 2>/dev/null || true)
    if [ -n "$q" ]; then
      msg "$(jq -nc --arg id "$q" '{kind:"agent_question_answer",id:$id,action:"accept",answers:{word:{selected:["gamma"]},note:{text:"lower case, like alpha and beta"}}}')"
      answered=1
      echo "owner answered $q"
    fi
  fi
  # Done: the orchestrator ran check.sh (its output, not its source, which
  # a model may have read) and its turn is over.
  if grep -qE '(ok|FAIL) +A was accepted with trailers' "$state/agent-transcripts/$session.jsonl" 2>/dev/null && ! working; then break; fi
  # Three quiet minutes with the orchestrator idle: the owner says to go on.
  quiet=$(( now * 1000 - $(last_event) ))
  if [ "$prods" -lt 5 ] && [ "$quiet" -gt 180000 ] && ! working; then
    msg "$(jq -nc --arg k "$key" '{kind:"agent_prompt",key:$k,text:"Continue with the script."}')"
    prods=$((prods + 1))
    echo "owner prodded $prods"
  fi
  sleep 5
done

./check.sh >"$out/check.txt" 2>&1 || true
cat "$out/check.txt"
cp -a "$state/agent-transcripts" "$out/transcripts" 2>/dev/null || true
cp "$state/workspaces.json" "$out/" 2>/dev/null || true
cp -a .wash "$out/dot-wash"
git log --stat >"$out/git-log.txt"
python3 /opt/jail/audit.py "$work" >"$out/audit.txt" 2>&1 || true
python3 /opt/jail/audit.py --costs "$work" >"$out/costs.json" 2>/dev/null || echo '{"total_usd":null}' >"$out/costs.json"
jq -n \
  --argjson catalog "$CATALOG" --argjson timed_out "$timed_out" --argjson prods "$prods" \
  --argjson ok "$(grep -c '^ok ' "$out/check.txt" || true)" --argjson failed "$(grep -c '^FAIL ' "$out/check.txt" || true)" \
  --argjson minutes "$(awk "BEGIN{printf \"%.1f\", ($(date +%s) - $began) / 60}")" \
  --slurpfile costs "$out/costs.json" \
  --slurpfile ws "$out/workspaces.json" \
  '{catalog: $catalog.slots, timed_out: $timed_out, owner_prods: $prods, checks_ok: $ok, checks_failed: $failed,
    minutes: $minutes, cost_usd: $costs[0].total_usd, cost_by_model: $costs[0].by_model,
    wash_reminders: ([$ws[0].workspaces[].messages[]? | select(.sender=="wash" and .type!="note")] | length),
    wash_notes: ([$ws[0].workspaces[].messages[]? | select(.sender=="wash" and .type=="note")] | length),
    failed_assignments: ([$ws[0].workspaces[].assignments[]? | select(.state=="failed")] | length)}' >"$out/scorecard.json"
# Transcripts store a streamed message whole at every chunk: large, and
# nine tenths redundant. Kept complete, compressed.
find "$out" -name '*.jsonl' -exec gzip -9 {} +
tail -1 "$out/audit.txt"
cat "$out/scorecard.json"
