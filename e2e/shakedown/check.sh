#!/bin/sh
# Checks the shakedown's end state on disk. Prints one line per check and
# exits non-zero if any failed.
cd "$(dirname "$0")" || exit 2
fail=0
check() { if eval "$2" >/dev/null 2>&1; then echo "ok   $1"; else echo "FAIL $1"; fail=1; fi; }
state() { awk -v id="$1" '$1=="id"{cur=$3} $1=="state" && cur=="\""id"\""{print $3}' .wash/plan.toml | tr -d '"'; }
check "words/alpha.txt says alpha"            'grep -qx alpha words/alpha.txt'
check "words/beta.txt says beta"              'grep -qx beta words/beta.txt'
check "words/gamma.txt has the owner's word"  'grep -qxi gamma words/gamma.txt'
check "the plan file is Wash's"               'head -1 .wash/plan.toml | grep -q "wash-plan"'
for n in M1 M2 A B C; do check "node $n is done" "[ \"\$(state $n)\" = done ]"; done
check "node M3 is active"                     '[ "$(state M3)" = active ]'
check "node C records its override"           'grep -q "overrides" .wash/plan.toml'
check "the QA thread B-case has a file"       'test -f .wash/qa/B-case.md'
check "B-case holds the question and answer"  'grep -q "lower case" .wash/qa/B-case.md'
check "the owner's answers are in C-owner"    'grep -q "Owner decision" .wash/qa/C-owner.md'
check "no thread file names a home directory" '! grep -rq "$HOME" .wash/qa .wash/plan.toml'
check "A was accepted with trailers"          'git log --format=%B | grep -q "^Plan-Node: A"'
check "the trailers name A's review"          'git log --format=%B | grep -q "^Reviewed-by: "'
check "handoffs stay out of git"              '! git ls-files | grep -q "^.wash/local/"'
exit $fail
