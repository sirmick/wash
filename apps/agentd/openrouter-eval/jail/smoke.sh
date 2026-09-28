#!/bin/sh
# The jail's smoke test, run inside the container: a headless router, and the
# Agent app driven only through the control socket (launch, msg, watch), as
# its frontend would drive it, against the fake ACP adapter. It proves the
# path a real run takes, with no browser, no model and no key.
set -eu
log=/tmp/jail-smoke
mkdir -p "$log" /home/agent/work
sock=/tmp/wash-$(id -u).sock

wash-router -http -no-auth -no-session -listen 127.0.0.1:11000 -apps-dir /opt/wash >"$log/router.log" 2>&1 &
for _ in $(seq 50); do [ -S "$sock" ] && break; sleep 0.2; done
[ -S "$sock" ] || { echo "FAIL router never opened $sock"; cat "$log/router.log"; exit 1; }

ctl() { printf '%s\n' "$1" | socat -t 10 - "UNIX-CONNECT:$sock"; }
msg() { r=$(ctl "{\"t\":\"msg\",\"instance_id\":\"$inst\",\"data\":$1}"); [ "$(echo "$r" | jq -r .t)" = msg.ok ] || { echo "FAIL msg $1: $r"; exit 1; }; }
# waitfor JQ SECONDS: the first watched message matching the jq filter.
waitfor() {
  for _ in $(seq $(($2 * 5))); do
    hit=$(jq -c "select(.t==\"msg\") | .data | select($1)" "$log/watch.jsonl" 2>/dev/null | head -1)
    [ -n "$hit" ] && { echo "$hit"; return 0; }
    sleep 0.2
  done
  return 1
}
check() { if [ -n "$2" ]; then echo "ok   $1"; else echo "FAIL $1"; tail -20 "$log/router.log"; exit 1; fi; }

inst=$(ctl '{"t":"launch","app_id":"com.wash.ai"}' | jq -r '.instance_id // empty')
check "launch com.wash.ai with no browser (instance $inst)" "$inst"

# The watch: its request, then held open (the sleep) and read to a file.
(printf '{"t":"watch","instance_id":"%s"}\n' "$inst"; sleep 100000) | socat - "UNIX-CONNECT:$sock" >"$log/watch.jsonl" &
for _ in $(seq 25); do grep -q '"watching"' "$log/watch.jsonl" 2>/dev/null && break; sleep 0.2; done
check "watch is streaming" "$(grep '"watching"' "$log/watch.jsonl" || true)"

msg '{"kind":"agent_start","agent":"codex","cwd":"/home/agent/work","prompt":"ask","claim":true}'
started=$(waitfor '.kind=="agent_started"' 30 || true)
key=$(echo "$started" | jq -r '.key // empty')
check "agent_start through msg; agent_started through watch (key $key)" "$key"

msg "{\"kind\":\"transcript_subscribe\",\"key\":\"$key\"}"
ask=$(waitfor "[.. | objects | select(has(\"suggested_rule\") or (has(\"tool\") and has(\"row_key\")))] | length > 0" 30 || true)
askid=$(echo "$ask" | jq -r '[.. | objects | select(has("row_key") and has("tool"))][0].id // empty')
check "the permission ask arrives through watch (id $askid)" "$askid"

msg "{\"kind\":\"agent_answer\",\"id\":\"$askid\",\"decision\":\"allow\",\"remember\":false,\"rule\":\"\"}"
answered=$(waitfor '.kind=="transcript_event" and (.event.text // "" | test("allow"; "i"))' 30 || true)
check "answering it through msg; the agent's reply names the answer" "$answered"

msg "{\"kind\":\"agent_prompt\",\"key\":\"$key\",\"text\":\"hello again\"}"
second=$(waitfor '.kind=="transcript_event" and .event.kind=="user" and (.event.text // "" | test("hello again"))' 30 || true)
check "a second prompt through msg reaches the transcript" "$second"

echo "all ok ($(grep -c . "$log/watch.jsonl") messages watched)"
