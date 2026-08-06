# Flake log — observed test flakes, dated

The running record of flakes actually *seen*, with the evidence needed to
tell "my change broke it" from "this tier has always been like this".
`docs/TEST_FLAKES.md` is the fix plan (the 2026-07-03 audit, phases A–E);
this file is the log that feeds it. Add an entry whenever a suite goes red
on something you didn't write.

**The rule this file exists to enforce:** before blaming (or absolving)
your branch, get a baseline. Rebuild the tree at the pre-change commit and
run the same subset the same number of times. A single green baseline run
proves nothing about a flake that fires one run in three.

Entry template: date · tree · what failed · how often · mechanism (if
known) · verdict · where the fix lives.

---

## 2026-08-06 — `reconnect` same-port restart, on the agent-app branch

Full suite on `branches/agent-app` (30 commits: the ACP pivot) came back
448 passed / 3 failed. Two were the standing display-tier pair below.
The third was new to me:

| spec | isolated runs |
|---|---|
| `reconnect.spec.ts` › router drops → banner + Reconnect-now → same-port restart recovers | 3/3 failed |

Deterministic, so not a flake — and the branch touches the pty
environment and the session FE, both plausible. **A/B against baseline
`1cba66a` (main, the branch point), same spec, freshly built worktree:
also fails.**

Verdict: **pre-existing, not the branch.** Test times out at 30s waiting
for the banner to clear after the replacement router binds the same port;
whether the second router fails to bind or the shell fails to re-dial is
not yet established. Nothing on agent-app goes near it.

Fix lives: unassigned. Worth its own look — a deterministic red is
cheaper to chase than the load-dependent ones above, and this one costs a
merge gate every time.

Cleaned up in passing: the e2e fixture staged `wash-agent-hook` into
every router's apps dir, which M5 deleted, so every router logged
`disabled …/wash-agent-hook (?): probe…` at startup. Not the cause.

---

## 2026-07-29 — display capstone tier, under parallel load

**Tree:** `agent-term-m1` (AGENT_TERM.md M1) vs baseline `3f37903`
(pre-M1). Host: buzz, 32 cores, 8 workers, `WASH_E2E_MULTICALL=1`.

**Seen during:** `make e2e-test` for the M1 gate. Two full-suite runs went
red on display specs; a third was clean.

| Run | Failures | Passed |
|---|---|---|
| full suite #1 | `display-qt-popover`, `single-file` (term bundle cap — real, mine, fixed) | 422 |
| full suite #2 | `display-qt-popover`, `display-term-xclock` | 422 |
| full suite #3 | — | 424 |

**A/B, `playwright test display` (13 specs, whole tier in parallel):**

| Tree | run 1 | run 2 | run 3 |
|---|---|---|---|
| M1 (`02ac70d`) | 13 passed (8.1s) | `display-term-xclock` (21.2s) | `display-input-smoke` + `display-term-xclock` (21.1s) |
| baseline (`3f37903`) | 13 passed (9.1s) | `display-guest` (wayland) + `display-input-smoke` (27.3s) | `display-input-smoke` (21.1s) |

**Verdict: pre-existing.** Both trees fail ~2 runs in 3, at the same rate,
in the same tier, and the baseline's failing set includes specs
(`display-input-smoke`, `display-guest`) that touch nothing M1 changed.
Every one of them passes when run alone (`display-term-xclock` alone:
504ms). `display-input-smoke` is already tracked as **issue #7**; the tier
is `docs/TEST_FLAKES.md` **C5**.

**Signature to look for:** a red run of the display subset takes 21–27s
against 8–9s for a green one. One compositor stalls, its spec eats the 25s
timeout, and the rest of the tier finishes normally. Wall-clock alone tells
you which kind of run you had.

**Mechanism, `display-term-xclock` (new — not in C5's diagnosis):** the
failure is not a lost keystroke, it's `DISPLAY` never reaching the shell.
The buffer shows the command typed and echoed, then:

```
$ xclock
Error: Can't open display:
```

An *empty* display name means `DISPLAY` was unset at pty spawn, i.e. the
router's `spawnEnv` had no `WASH_X_DISPLAY` yet when the terminal started.
The spec guards against exactly this by waiting for `Xwayland ready on
DISPLAY=` and then `env.publish from` before launching the term — but
`waitForLog` matches from t=0 (**A10**), and the compositor emits *three*
`env.publish` events, the first of which can predate Xwayland having a
display to publish. So the second wait is satisfied by a stale earlier
match and the term is launched too soon. Under load the gap widens and the
race opens.

Fix shape (belongs with C5/A10, not with a feature branch): give
`waitForLog` a cursor and wait for an env.publish *after* the Xwayland
line, or better, assert the fact rather than the event — poll the router
log for the merged `WASH_X_DISPLAY` value before launching the terminal.

## 2026-08-02 — my own agent specs, once, under full-suite load

`term-agent-roster` ("closing the tab retracts its row immediately") timed
out waiting for its first roster row during a full `make e2e-test`; 3/3
green when run alone, and the artifact was cleaned by the passing re-runs
before it could be read. Unconfirmed but the mechanism fits **C5**: these
specs expand a sidebar section (moving keyboard focus off the pty) and then
type a command, so a click landing mid-re-render can swallow the leading
keystrokes — after which the test waits 15s for the effect of a command
that never ran.

Hardened rather than retried: `run()` now asserts the command echoed before
pressing Enter (a dropped keystroke fails immediately and legibly instead
of as a timeout), and the M6 spec's ~250-character JSON payload moved into
a script file so the typed line is short. A first attempt at *retyping* on
failure was backed out — it could type over a line whose Ctrl+U had not
landed yet, which is a worse failure than the one it was fixing.

**Not reproduced here, still open:** why the compositor stalls in the first
place when several run concurrently. The 21s-vs-8s split says the stall is
the primary event and the timeouts are downstream of it.

**Recurrences (all inside the cleared set above, all green in isolation):**
`agent-term-m4` gate — clean. `agent-term-m5` gate — clean. Post-merge
verification of main at `ba5b065` — `display-qt-popover` failed in the full
suite, passed alone in 8.2s. Six full-suite runs across M1–M5: four clean,
two red on display specs only.

**Earlier recurrence, `agent-term-m2` gate:** `make e2e-test` red again
with `display-guest` (wayland) + `display-term-xclock`; both pass in
isolation (2.4s for all three). Same tier, same signature, a branch that
touches notifications and the session/shell FE. No new baseline run needed —
the failing set is inside the set the A/B above already cleared. Treat any
red confined to `display-*` as this entry until the C5/A10 fixes land; a
failure outside that tier deserves its own baseline.

## 2026-08-02 — `internal/loopback` TestSpine: fixed, not flaky any more

Hit as a hard `make push` blocker during the 0.11.0 release gate:
`--- FAIL: TestSpine … bundle bytes mismatch: ""`, reproducing **5/5** under
`-race` (this box makes the race deterministic rather than occasional).

**A/B against baseline:** fails identically in a temp worktree at
`origin/main` (4f393db), i.e. before any of the M1–M7 agent-terminal work.
Pre-existing — the tracked issue #8 / TEST_FLAKES **B2**.

**Root cause (three layers, all harness-side except one product addition):**
the test completed the bundle on the `ChannelUnbind`, but bundle payload is
Bulk class and the unbind is a ctrl frame, so under strict priority the
unbind legitimately overtakes the data (`replayBundleToShell` says so in a
comment). Completing on the bind's `Size` exposed a second bug — the wait
loop called `nextCtrl()`, which blocks until a *ctrl* frame, so once the
final Bulk frame was the last thing on the wire the loop never re-tested and
hung to the package timeout. A third: raw frames arriving before their bind
were dropped outright. Fixed by `readOne`/`nextCtrl` split, `pendingRaw`
buffering, and byte-count completion. Product side gained
`wire.ErrUnknownCtrl` so a receiver can skip an unmodelled ctrl type
(`link.stats`) without treating it as corruption.

**Result:** `-race -count=20` green. B2 is closed rather than logged as a
recurrence — this one had a mechanism, not a load window.
