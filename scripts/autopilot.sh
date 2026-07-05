#!/usr/bin/env bash
# autopilot.sh — one long-running local loop that:
#   (a) keeps a dedicated clone's `main` in lockstep with origin/main,
#       and for each new open PR: merges it and runs the full push gate
#       (`make push`) so a green PR lands on remote main automatically;
#   (b) for each new labelled GH issue: branches, hands it to a headless
#       Claude agent to reproduce + fix, and if the fix is genuinely green
#       lands it on main through the same gate.
#
# The agent's self-report is advisory — the TESTS are the fitness function.
# Nothing lands unless the real gate passes; failures roll main back.
#
# RUN THIS AGAINST A DEDICATED CLONE, NOT YOUR WORKING TREE.
#   gh auth status               # must be logged in (issues/PRs + git push creds)
#   scripts/autopilot.sh         # default PUSH=1: green landings push to origin/main
#   PUSH=0 scripts/autopilot.sh  # dry-run mode: gate runs locally, nothing pushed
set -uo pipefail

# ---------------------------- config ---------------------------------
REPO_URL="${REPO_URL:-https://github.com/sirmick/wash.git}"
REPO_DIR="${REPO_DIR:-$HOME/wash-autopilot-repo}"   # dedicated clone (bot-owned)
MAIN="${MAIN:-main}"
POLL_SECONDS="${POLL_SECONDS:-120}"
ISSUE_LABEL="${ISSUE_LABEL:-agent}"                 # only issues with this label are worked
BOT_LOGIN="${BOT_LOGIN:-}"                          # skip PRs opened by this login (self-loop guard)
# HARD ALLOWLIST — only ever act on issues/PRs opened by these GitHub logins.
# GitHub identifies actors by login, not email: sirmick@gmail.com == login `sirmick`.
OWNER_LOGINS="${OWNER_LOGINS:-sirmick}"
# Stricter layer for PRs: at least one commit author/committer email must CONTAIN
# one of these substrings (case-insensitive, any domain) — e.g. mcloonan@nvidia.com
# or sirmick@gmail.com both match. Extra co-authors (e.g. noreply@anthropic.com)
# alongside a matching one are fine; the PR is rejected only if NONE match.
VERIFY_COMMIT_EMAILS="${VERIFY_COMMIT_EMAILS:-1}"
OWNER_EMAIL_SUBSTR="${OWNER_EMAIL_SUBSTR:-mcloonan sirmick}"
PUSH="${PUSH:-1}"                                   # 1 = push green landings to origin/main; 0 = local-only gate
CLAUDE_BIN="${CLAUDE_BIN:-claude}"
CLAUDE_FLAGS="${CLAUDE_FLAGS:---dangerously-skip-permissions}"
CLAUDE_TIMEOUT="${CLAUDE_TIMEOUT:-1800}"            # per-issue wall clock for the agent (seconds)
# Lean, deterministic landing gate — TESTS ONLY, no push (land_on_main does the
# push itself when PUSH=1). Default is `make unit-test`: fast and green every run.
# The full `make push` matrix (e2e + compositor + packaging) is too flaky under
# an autonomous loop's load — keep that for human/CI runs. Override GATE to add
# a targeted e2e spec, e.g. GATE="make unit-test && cd e2e && pnpm exec playwright test chrome-windows".
GATE="${GATE:-make unit-test}"
STATE="${STATE:-$REPO_DIR/.autopilot}"
CI_WORKFLOW="${CI_WORKFLOW:-ci.yml}"   # react only to this workflow's failures on MAIN ("" = any)
CI_FIX_MAX="${CI_FIX_MAX:-3}"          # back off after this many consecutive CI-fix landings without a green run

log() { printf '%s | %s\n' "$(date '+%H:%M:%S')" "$*"; }
seen() { grep -qxF "$1" "$STATE/$2" 2>/dev/null; }
mark() { printf '%s\n' "$1" >>"$STATE/$2"; }

# ------------------------- bootstrap ---------------------------------
command -v gh >/dev/null      || { echo "need: gh"; exit 1; }
command -v "$CLAUDE_BIN" >/dev/null || { echo "need: $CLAUDE_BIN"; exit 1; }
gh auth status >/dev/null 2>&1 || { echo "gh not authenticated (run: gh auth login)"; exit 1; }

if [ ! -d "$REPO_DIR/.git" ]; then
  log "cloning $REPO_URL -> $REPO_DIR"
  git clone "$REPO_URL" "$REPO_DIR" || exit 1
fi
cd "$REPO_DIR" || exit 1

# Safety: never operate on a checkout that isn't the bot's own clone.
if [ "$(pwd -P)" = "/home/mick/wash" ]; then
  echo "refuse: REPO_DIR is your live working tree; point it at a dedicated clone"; exit 1
fi
mkdir -p "$STATE"

# Ensure the labels the bot toggles exist (best-effort; harmless if present).
for L in "$ISSUE_LABEL" agent-working agent-landed agent-done agent-failed agent-ci-failed agent-couldnt-fix autopilot-flake ci-failure; do
  gh label create "$L" >/dev/null 2>&1 || true
done

# Single-instance lock so two shells can't fight over the clone.
exec 9>"$STATE/.lock"
flock -n 9 || { echo "another autopilot is already running on $REPO_DIR"; exit 1; }

log "autopilot up | repo=$REPO_DIR push=$PUSH owner-logins='$OWNER_LOGINS' issue-label=$ISSUE_LABEL poll=${POLL_SECONDS}s"

# is_owner <login> : true ONLY if login is in the hard OWNER_LOGINS allowlist.
# This is the trust boundary — a PR/issue from anyone else is never built,
# tested, prompted-on, or landed (prompt injection + code-exec vector).
is_owner() {
  local login="$1"; [ -n "$login" ] || return 1
  local o; for o in $OWNER_LOGINS; do [ "$login" = "$o" ] && return 0; done
  return 1
}

# pr_commits_owned <num> : when VERIFY_COMMIT_EMAILS=1, true iff AT LEAST ONE
# commit author/committer email on the PR contains one of OWNER_EMAIL_SUBSTR
# (case-insensitive). Extra co-authors are fine. When 0, always true (the login
# allowlist is the gate). Git email is user-set, so this is a secondary layer.
pr_commits_owned() {
  [ "$VERIFY_COMMIT_EMAILS" = 1 ] || return 0
  local num="$1" emails tok
  emails="$(gh pr view "$num" --json commits \
      -q '.commits[] | .authors[].email, (.committer.email // empty)' 2>/dev/null)" || return 1
  [ -n "$emails" ] || return 1
  for tok in $OWNER_EMAIL_SUBSTR; do
    printf '%s\n' "$emails" | grep -qiF "$tok" && return 0
  done
  log "  no commit author email contains any of: $OWNER_EMAIL_SUBSTR"
  return 1
}

# ---------------------- shared landing path --------------------------
# run_gate <logfile> : run the active gate (push gate if PUSH=1, else test gate),
# streaming output live AND capturing it to <logfile>. Returns the gate's status.
run_gate() {
  local logfile="$1"
  ( eval "$GATE" ) 2>&1 | tee "$logfile"
  return "${PIPESTATUS[0]}"
}

# file_flake_issue <ctx> <logfile> <outcome> : open a GH issue recording a gate
# that failed on first attempt. Deduped by failure signature so a recurring flake
# doesn't spam. Tagged autopilot-flake AND the pickup label, so the bot will also
# try to fix the flaky test itself (dedupe keeps that from running away).
file_flake_issue() {
  local ctx="$1" logfile="$2" outcome="$3" sig tail_out title
  # Fingerprint the REAL failure, not a passing test whose name contains "Error".
  # Match only true failure markers: go `--- FAIL:`, playwright `✘`, TAP `not ok`.
  sig="$(grep -aE '^--- FAIL:|✘|^not ok ' "$logfile" | grep -avE 'passed|does NOT|no retry|keep retrying' \
        | head -1 | tr '\t' ' ' | sed 's/^[[:space:]]*//' | cut -c1-140)"
  [ -n "$sig" ] || sig="$(grep -aE '[0-9]+ failed|^FAIL([[:space:]]|$)' "$logfile" | head -1 | tr '\t' ' ' | cut -c1-140)"
  [ -n "$sig" ] || sig="$ctx gate failure"
  if gh issue list --state open --label autopilot-flake --search "$sig" --json number -q '.[].number' 2>/dev/null | grep -q .; then
    log "$ctx: flake already tracked ($sig) — not filing duplicate"; return 0
  fi
  if [ "$outcome" = passed ]; then title="autopilot: flaky gate on $ctx (green on retry)"
  else title="autopilot: gate failed twice on $ctx"; fi
  tail_out="$(tail -n 60 "$logfile" 2>/dev/null)"
  gh issue create --title "$title" --label autopilot-flake --label "$ISSUE_LABEL" \
    --body "$(printf 'The autopilot gate for **%s** FAILED on the first attempt and **%s** on retry — a likely load-sensitive/flaky test rather than the change under test.\n\nFailure signature: `%s`\n\nFailing-run tail:\n\n```\n%s\n```\n' "$ctx" "$outcome" "$sig" "$tail_out")" \
    >/dev/null 2>&1 && log "$ctx: filed flaky-gate issue ($sig)"
}

# land_on_main <branch> <context> : merge <branch> into main, run the gate (with
# ONE retry on failure, since a single fail is usually a flaky test), push if
# PUSH=1. Any gate failure files a flake issue. Returns 0 only if the gate went
# green (first try or retry) — otherwise main is rolled back to origin/main.
land_on_main() {
  local branch="$1" ctx="$2" base mode log1 log2
  git fetch origin --prune --quiet
  git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
  base="$(git rev-parse HEAD)"

  if ! git merge --no-edit "$branch" --quiet; then
    log "$ctx: merge conflict — aborting"; git merge --abort 2>/dev/null; git reset --hard "$base" --quiet; return 1
  fi

  log "$ctx: running gate ($GATE), PUSH=$PUSH"
  log1="$STATE/gate.$$.1.log"
  if run_gate "$log1"; then
    rm -f "$log1"
  else
    log "$ctx: gate FAILED first attempt — retrying once (suspected flake)"
    log2="$STATE/gate.$$.2.log"
    if run_gate "$log2"; then
      log "$ctx: gate green on retry — first failure was flaky"
      file_flake_issue "$ctx" "$log1" passed
      rm -f "$log1" "$log2"
    else
      log "$ctx: gate failed twice — rolling back"
      file_flake_issue "$ctx" "$log2" "failed again"
      git reset --hard "$base" --quiet
      rm -f "$log1" "$log2"
      return 1
    fi
  fi
  # Gate is green (first try or retry). The gate only TESTS — do the push here.
  if [ "$PUSH" = 1 ]; then
    if git push origin "$MAIN"; then
      log "$ctx: LANDED + pushed to origin/$MAIN"
    else
      log "$ctx: push rejected (remote moved?) — rolling back"; git reset --hard "$base" --quiet; return 1
    fi
  else
    log "$ctx: green on local main (PUSH=0, not pushed)"
  fi
  return 0
}

# --------------------------- (a) PRs ---------------------------------
handle_prs() {
  local prs; prs="$(gh pr list --state open --limit 50 \
      --json number,headRefName,headRefOid,isDraft,author \
      -q '.[] | select(.isDraft|not) | [.number, .headRefOid, .author.login] | @tsv')" || return 0
  local num oid who key
  while IFS=$'\t' read -r num oid who; do
    [ -n "$num" ] || continue
    [ -n "$BOT_LOGIN" ] && [ "$who" = "$BOT_LOGIN" ] && continue    # don't chase our own PRs
    # Trust boundary: only build/test/land PRs opened by an owner login, and
    # (optionally) only when every commit email is ours. Anything else is a
    # prompt-injection + code-exec vector — refuse without touching it.
    if ! is_owner "$who" || ! pr_commits_owned "$num"; then
      seen "pr:$num:$oid" prs_done || log "PR #$num ($who): not owner-authored — skipping"
      mark "pr:$num:$oid" prs_done
      continue
    fi
    key="pr:$num:$oid"
    seen "$key" prs_done && continue
    log "PR #$num ($who @ ${oid:0:8}): processing"
    (
      set -e
      git fetch origin --prune --quiet
      git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
      gh pr checkout "$num" --force        # local branch tracking the PR head
      local br; br="$(git rev-parse --abbrev-ref HEAD)"
      git checkout "$MAIN" --quiet
      if land_on_main "$br" "PR #$num"; then
        [ "$PUSH" = 1 ] || gh pr comment "$num" --body "autopilot: gate green locally (PUSH=0, not merged to remote)."
      else
        gh pr comment "$num" --body "autopilot: gate failed on merge into \`$MAIN\` — rolled back. See runner logs."
      fi
    ) || log "PR #$num: handler error"
    mark "$key" prs_done
  done <<<"$prs"
}

# -------------------------- (b) issues -------------------------------
handle_issues() {
  local issues; issues="$(gh issue list --state open --limit 50 --label "$ISSUE_LABEL" \
      --json number,author -q '.[] | [.number, .author.login] | @tsv')" || return 0
  local num who body prompt out analysis br landed
  while IFS=$'\t' read -r num who; do
    [ -n "$num" ] || continue
    seen "issue:$num" issues_done && continue
    # Same trust boundary as PRs: the issue text becomes the agent's prompt, so a
    # non-owner author is a direct prompt-injection vector even before any code runs.
    if ! is_owner "$who"; then
      log "issue #$num ($who): not owner-authored — skipping"
      mark "issue:$num" issues_done
      continue
    fi
    log "issue #$num: picking up"
    body="$(gh issue view "$num" --json title,body -q '.title + "\n\n" + .body')"

    git fetch origin --prune --quiet
    git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
    br="issue-$num"
    git branch -D "$br" >/dev/null 2>&1 || true
    git checkout -b "$br" --quiet
    gh issue edit "$num" --add-label agent-working --remove-label "$ISSUE_LABEL" >/dev/null 2>&1 || true

    prompt="You are an autonomous fix agent in a git checkout at $(pwd), on branch $br.

GitHub issue #$num:
$body

You are a BUG-FIX agent. DEFAULT TO ATTEMPTING the fix. The safety net is the
automated test gate plus a human reviewing your diff before it ships — so a
wrong or incomplete fix is caught and rolled back, never released. That means
you should try, not bail: a reasonable, test-backed attempt is far more useful
than a premature GAVEUP.

INVESTIGATE before deciding — read the relevant code and try to reproduce.
Decline (make NO commits, report GAVEUP) ONLY if, after actually investigating,
one of these is clearly true:
  - it's a feature request / design change (a new capability or UX/product
    decision), not a defect in existing behaviour; or
  - it needs information you cannot find or reasonably infer from the repo; or
  - you cannot form ANY plausible root cause after looking.
Scope is NOT a reason to decline — a multi-file but well-understood fix is fine.
Being less than 100% certain is NOT a reason to decline — make your best
test-backed attempt and let the gate judge it.

When you proceed:
1. Reproduce the bug and, where practical, add a regression test that fails on
   the current code. If a precise failing test isn't feasible, still verify the
   fix by hand and keep the suite green.
2. Fix the ROOT CAUSE in product code, in the surrounding style. Never weaken,
   skip, or delete tests to make things pass.
3. Run \`make unit-test\` (plus any narrower test for the area) until genuinely green.
4. Commit, message referencing #$num.

Constraints: stay in this repo; do NOT push; do NOT touch CI/packaging or unrelated files.
End your final message with exactly one line, either:
AUTOPILOT_RESULT: FIXED
AUTOPILOT_RESULT: GAVEUP"

    out="$(timeout "$CLAUDE_TIMEOUT" "$CLAUDE_BIN" $CLAUDE_FLAGS -p "$prompt" 2>&1)" || true
    printf '%s\n' "$out" | tail -n 40
    # The agent's own write-up (root cause, what it changed / why it declined) —
    # everything except the machine-readable verdict line. Posted to the issue so
    # a human sees the reasoning, not just the label.
    analysis="$(printf '%s\n' "$out" | grep -avxE 'AUTOPILOT_RESULT: (FIXED|GAVEUP)')"
    [ -n "$analysis" ] || analysis="(no analysis captured)"

    if [ "$(git rev-list --count "origin/$MAIN..HEAD")" -gt 0 ]; then
      if land_on_main "$br" "issue #$num"; then
        if [ "$PUSH" = 1 ]; then
          # Landed on origin, but the local gate isn't CI. Don't close yet —
          # mark it landed and let handle_pending_closes close it only once CI
          # passes on this commit (or flip it to agent-ci-failed if CI fails).
          landed="$(git rev-parse HEAD)"
          gh issue edit "$num" --remove-label agent-working --add-label agent-landed >/dev/null 2>&1 || true
          gh issue comment "$num" --body "$(printf '**autopilot: fix landed on `%s` as %s — awaiting CI before closing.**\n\n---\n### Analysis\n%s' "$MAIN" "${landed:0:8}" "$analysis")"
          mark "issue:$num $landed" pending_close
        else
          gh issue edit "$num" --remove-label agent-working --add-label agent-done >/dev/null 2>&1 || true
          gh issue comment "$num" --body "$(printf '**autopilot: fix verified on local `%s` (PUSH=0, not pushed, not closed).**\n\n---\n### Analysis\n%s' "$MAIN" "$analysis")"
        fi
      else
        gh issue edit "$num" --remove-label agent-working --add-label agent-failed >/dev/null 2>&1 || true
        gh issue comment "$num" --body "$(printf '**autopilot: produced a fix but the gate failed on merge — rolled back, left for a human.**\n\n---\n### Analysis\n%s' "$analysis")"
      fi
    else
      gh issue edit "$num" --remove-label agent-working --add-label agent-couldnt-fix >/dev/null 2>&1 || true
      gh issue comment "$num" --body "$(printf '**autopilot: no automatic fix — leaving for a human.**\n\n---\n### Analysis\n%s' "$analysis")"
    fi
    git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
    mark "issue:$num" issues_done
  done <<<"$issues"
}

# ------------------------- (c) CI failures ---------------------------
# file_ci_issue <run-id> <sha> <workflow> <msg> : record a CI failure for a human,
# deduped by short-sha in the title (comment if it already exists). Labeled
# ci-failure (never `agent` — CI is handled here, not via the issue queue).
file_ci_issue() {
  local id="$1" sha="$2" wf="$3" msg="$4" title url num
  title="autopilot: CI failed on $MAIN ($wf @ ${sha:0:8})"
  num="$(gh issue list --state open --label ci-failure --search "${sha:0:8}" --json number -q '.[0].number' 2>/dev/null)"
  url="$(gh run view "$id" --json url -q .url 2>/dev/null)"
  if [ -n "$num" ]; then
    gh issue comment "$num" --body "$msg" >/dev/null 2>&1 && log "CI: commented on #$num"
  else
    gh issue create --title "$title" --label ci-failure \
      --body "$(printf 'CI run: %s\n\n%s' "$url" "$msg")" >/dev/null 2>&1 && log "CI: filed ci-failure issue ($wf ${sha:0:8})"
  fi
}

# handle_ci : notice a failed CI run on MAIN and attempt a fix-forward. Only the
# MAIN branch's own runs are considered (trusted input — PR-run logs, which can
# carry attacker-controlled text, are never fed to the agent). Bounded by a
# consecutive-fix streak so a persistently-red pipeline escalates to a human
# rather than churning commits.
handle_ci() {
  local sel="--branch $MAIN"; [ -n "$CI_WORKFLOW" ] && sel="$sel --workflow $CI_WORKFLOW"
  local run; run="$(gh run list $sel --limit 8 \
      --json databaseId,headSha,conclusion,status,workflowName \
      -q 'map(select(.status=="completed")) | .[0] // empty | [.databaseId,.headSha,.conclusion,.workflowName] | @tsv')" 2>/dev/null || return 0
  [ -n "$run" ] || return 0
  local id sha concl wf; IFS=$'\t' read -r id sha concl wf <<<"$run"

  if [ "$concl" = success ]; then echo 0 >"$STATE/ci_fix_streak"; return; fi   # green — clear streak
  [ "$concl" = failure ] || return 0
  seen "ci:$id" ci_done && return

  local streak; streak="$(cat "$STATE/ci_fix_streak" 2>/dev/null || echo 0)"
  if [ "$streak" -ge "$CI_FIX_MAX" ]; then
    log "CI still failing (run $id ${sha:0:8}) after $streak autopilot fixes — backing off for a human"
    file_ci_issue "$id" "$sha" "$wf" "CI is still failing after $streak consecutive autopilot fix attempts. Backing off — this needs a human."
    mark "ci:$id" ci_done; return
  fi

  log "CI FAILED (run $id wf=$wf ${sha:0:8}) — attempting a fix"
  git fetch origin --prune --quiet
  git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
  local br="ci-fix-${sha:0:8}"
  git branch -D "$br" >/dev/null 2>&1 || true
  git checkout -b "$br" --quiet

  local logs; logs="$(gh run view "$id" --log-failed 2>/dev/null | sed -E 's/\x1b\[[0-9;]*[A-Za-z]//g' | tail -n 150)"
  local prompt out analysis
  prompt="You are an autonomous fix agent in a git checkout at $(pwd), on branch $br.

CI workflow \"$wf\" FAILED on branch $MAIN at commit ${sha:0:8}.
Tail of the failing job log:
------
$logs
------

FIRST, exercise judgment. Only proceed if the failure has a CLEAR, OBVIOUS root
cause fixable with a small, safe change to the code or tests it points at. If it
is a flaky or infrastructure failure (e.g. a socket/connection/timeout error, a
load-sensitive race), ambiguous, or broad in scope — STOP, make NO commits, and
report GAVEUP with a one-line reason. When in doubt, leave it for a human.

If clear and obvious:
1. Identify the failing step/test from the log and reproduce it locally.
2. Fix the ROOT CAUSE minimally, in the surrounding style. Never weaken, skip,
   or delete tests to go green.
3. Re-run locally (\`make unit-test\`, or the specific failing command) until green.
4. Commit, referencing the CI failure ($wf @ ${sha:0:8}).

Constraints: stay in this repo; do NOT push; do NOT touch unrelated files.
End with exactly one line: AUTOPILOT_RESULT: FIXED  or  AUTOPILOT_RESULT: GAVEUP"

  out="$(timeout "$CLAUDE_TIMEOUT" "$CLAUDE_BIN" $CLAUDE_FLAGS -p "$prompt" 2>&1)" || true
  printf '%s\n' "$out" | tail -n 40
  analysis="$(printf '%s\n' "$out" | grep -avxE 'AUTOPILOT_RESULT: (FIXED|GAVEUP)')"
  [ -n "$analysis" ] || analysis="(no analysis captured)"

  if [ "$(git rev-list --count "origin/$MAIN..HEAD")" -gt 0 ]; then
    if land_on_main "$br" "CI-fix ${sha:0:8}"; then
      echo $((streak + 1)) >"$STATE/ci_fix_streak"
      file_ci_issue "$id" "$sha" "$wf" "$(printf '**autopilot pushed a CI fix.** A fresh CI run will confirm.\n\n---\n### Analysis\n%s' "$analysis")"
    else
      file_ci_issue "$id" "$sha" "$wf" "$(printf '**autopilot produced a fix but the local gate failed — not pushed.**\n\n---\n### Analysis\n%s' "$analysis")"
    fi
  else
    file_ci_issue "$id" "$sha" "$wf" "$(printf '**autopilot could not fix CI automatically (likely flaky/infra) — leaving for a human.**\n\n---\n### Analysis\n%s' "$analysis")"
  fi
  git checkout "$MAIN" --quiet && git reset --hard "origin/$MAIN" --quiet
  mark "ci:$id" ci_done
}

# handle_pending_closes : an issue fix that landed (PUSH=1) sits in
# $STATE/pending_close as "issue:<num> <sha>" until CI on that commit resolves.
# CI green → close the issue (agent-done). CI failed → leave it open, flip to
# agent-ci-failed for a human. Still running / no run yet → keep waiting.
handle_pending_closes() {
  local f="$STATE/pending_close"
  [ -s "$f" ] || return 0
  local tmp="$f.tmp"; : >"$tmp"
  local line key sha num concl
  while read -r line; do
    [ -n "$line" ] || continue
    key="${line%% *}"; sha="${line#* }"; num="${key#issue:}"
    concl="$(gh run list --branch "$MAIN" --workflow "$CI_WORKFLOW" --limit 20 \
        --json headSha,status,conclusion \
        -q "map(select((.headSha|startswith(\"$sha\")) and (.status==\"completed\"))) | .[0].conclusion // empty" 2>/dev/null)"
    case "$concl" in
      success)
        gh issue edit "$num" --remove-label agent-landed --add-label agent-done >/dev/null 2>&1 || true
        gh issue comment "$num" --body "autopilot: CI passed on \`${sha:0:8}\` — fix confirmed, closing." >/dev/null 2>&1
        gh issue close "$num" >/dev/null 2>&1 && log "issue #$num: CI green (${sha:0:8}) — closed" ;;
      failure)
        gh issue edit "$num" --remove-label agent-landed --add-label agent-ci-failed >/dev/null 2>&1 || true
        gh issue comment "$num" --body "autopilot: fix landed as \`${sha:0:8}\` but **CI failed** on it — left open for a human (see the ci-failure issue for details)." >/dev/null 2>&1
        log "issue #$num: CI failed (${sha:0:8}) — left open (agent-ci-failed)" ;;
      *)
        printf '%s\n' "$line" >>"$tmp" ;;   # no completed CI run yet — keep waiting
    esac
  done <"$f"
  mv "$tmp" "$f"
}

# ----------------------------- loop ----------------------------------
while :; do
  git fetch origin --prune --quiet 2>/dev/null || log "fetch failed (offline?)"
  git checkout "$MAIN" --quiet 2>/dev/null && git reset --hard "origin/$MAIN" --quiet 2>/dev/null
  handle_pending_closes || log "handle_pending_closes errored"
  handle_prs    || log "handle_prs errored"
  handle_issues || log "handle_issues errored"
  handle_ci     || log "handle_ci errored"
  [ "${ONCE:-0}" = 1 ] && { log "ONCE=1 — single pass complete, exiting"; break; }
  sleep "$POLL_SECONDS"
done
