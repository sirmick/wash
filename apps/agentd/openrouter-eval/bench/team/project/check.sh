#!/bin/sh
# Checks the team benchmark's process on disk (the code is scored apart, by
# held-back tests). Prints one line per check; exits non-zero if any failed.
cd "$(dirname "$0")" || exit 2
fail=0
check() { if eval "$2" >/dev/null 2>&1; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
state() { awk -v id="$1" '$1=="id"{cur=$3} $1=="state" && cur=="\""id"\""{print $3}' .wash/plan.toml | tr -d '"'; }
start=$(git rev-list --max-parents=0 HEAD)
check "go test ./... passes"                  'go test ./...'
check "no existing test file was changed"     '[ -z "$(git diff --name-only $start -- ini_test.go lru_test.go wrap_test.go)" ]'
for n in INI LRU WRAP; do
  check "node $n is done"                     "[ \"\$(state $n)\" = done ]"
  check "$n was accepted with trailers"       "git log --format='%(trailers:key=Plan-Node,valueonly)' | grep -qx $n"
done
check "every acceptance names a review"       '[ "$(git log --format="%(trailers:key=Reviewed-by,valueonly)" | grep -c .)" -ge 3 ]'
check "node BUILD is done"                    '[ "$(state BUILD)" = done ]'
check "node SHIP is active"                   '[ "$(state SHIP)" = active ]'
exit $fail
