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

**Not reproduced here, still open:** why the compositor stalls in the first
place when several run concurrently. The 21s-vs-8s split says the stall is
the primary event and the timeouts are downstream of it.
