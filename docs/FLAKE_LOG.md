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

## 2026-08-08 — agent-spec trio: two send/complete races, root-caused and fixed

**Seen during:** four consecutive CI `ci` runs on main/tags (0.13.0 →
0.13.1). Never the same spec twice in a row, always an agent spec stuck
with the fake adapter mid-turn:

```
0.13.0 main:  agent-fs.spec.ts:110  (terminal output never in transcript)
mount-merge:  agent-session.spec.ts:98 + agent-fs.spec.ts:168  (stuck "working…")
0.13.1 main:  term-wedge-recovery (see entry below)
0.13.1 tag:   agent-fs.spec.ts:110 again
```

The v0.13.0 *tag* run — same tree as the failing main run — was green,
which said "load window" from the start.

**Mechanism 1 — agentd `ReleaseTerminal` deleted the record before closing
the pty** (`apps/agentd/be/acpterm.go`). The transcript event is completed
by the pty's `onClose`, which fires from the pty→channel copy goroutine's
EOF drain — but `wait_for_exit` wakes on the *reaper's* `Done()`, which
closes earlier. An agent that runs `wait_for_exit → output → release`
(three fast local round trips) can beat the EOF drain; release then deleted
`termAll[id]` and the late `completeTerminalEvent` found nil and returned
silently. The event kept its dead channel, the FE mounted a live terminal
on it, and `agent-terminal-output` never rendered. Fix: close *then*
delete, plus a `termEarly` marker for the symmetric window where a command
exits before `CreateTerminal` has registered the event's seq.

**Mechanism 2 — the fake adapter sent before it listened**
(`e2e/fixtures/acp-fake/main.go`). Every `request(...)` + `await(id)` pair
wrote the JSON-RPC request first and registered the pending-reply channel
after; `deliver()` drops a response with no waiter. agentd answers in
sub-millisecond, so a preempted adapter goroutine lost the reply and the
turn hung its full 60s — the spec's "working…" screenshot. Fix: `request`
registers the slot before the bytes leave.

**Result:** agent-fs + agent-session at `--repeat-each=3` green (42/42).
Both were genuine races with mechanisms, not load noise — logged here
because the *presentation* (early-position specs failing on a busy runner,
green in isolation) looked exactly like the display-tier standing finding.

---

## 2026-08-08 (resolved) — the burst failure: a synchronous `term.reset()` jumping xterm's write queue

**Root-caused and fixed.** The two entries below were both looking at the
wrong half of the system — the router was never losing anything.

**Reproduced locally** (the squeeze the 2026-08-06 entry called for, turned
up: *one* core, three busy-loop hogs, everything `taskset -c 0`):
**7 failures in 21 runs (~33%)**. In isolation on an idle box it never
fails, which is why it only ever showed up on 2-core CI.

**The measurement that ended it.** Instrumenting the terminal FE to count
bytes in, bytes handed to `term.write()`, and bytes the write-callback
reported as parsed:

```
rendered:false   rx: 1067213   written: 1067213   parsed: 1067213
buf: { length: 16292, baseY: 16263, viewportY: 542, rows: 29 }
```

Every byte arrived, was written, and was fully parsed — and the last chunk
was the shell prompt that comes *after* the end marker. Nothing was lost
anywhere. Across runs the split is perfectly clean:

| | viewportY vs baseY |
|---|---|
| every passing run | `viewportY == baseY` (pinned to the bottom) |
| every failing run | `viewportY` ≈ 46–542 while `baseY` ≈ 14k–16k |

**The terminal was scrolled up, not wedged.** `toContainText` reads
xterm's rendered rows, i.e. the viewport — so the spec sat watching a
stale 29-row window ~15,000 lines above the output for its full 45s.

**Mechanism.** `xterm.write()` is asynchronous — it buffers and parses in
timed chunks. `term.reset()` is synchronous. The resync handler called
`term.reset()` directly, so a resync landing while a large replay was
still parsing **jumped the write queue** and reinitialised the buffer out
from under the parser mid-drain; `ydisp` then stopped tracking `ybase` and
the viewport never followed output again. A burst big enough to trigger
several resyncs in one second hit that window about a third of the time.
The FE comment claimed the ordering it needed ("run the resync callback
synchronously, BEFORE the snapshot bytes that follow") — true at the
dispatch layer, false inside xterm.

**Fix** (`web/lib/src/terminal.tsx`): issue the reset **in band**, as the
RIS escape `\x1bc` written through the same queue as the data, with the
mode re-seed concatenated onto it. xterm wires RIS to the same
`Terminal.reset()`, so the semantics are identical — but it now applies at
its true position in the stream, after the bytes before it and before the
snapshot after it. **20/20 green under the same squeeze** (10 probe runs
with the viewport instrumented, all `viewportY == baseY`; 10 runs of the
real spec, all ~4s where failures used to stall for 47s).

**Adjacent, unfixed:** `clearScreen: () => term?.clear()` (Edit → Clear)
is the same defect class — a synchronous xterm mutation that can jump the
queue. It is user-paced rather than machine-paced so it is much harder to
hit, and it has no e2e coverage, so it is left alone rather than changed
blind. The in-band equivalent would be writing `\x1b[3J\x1b[H\x1b[2J`.

**The lesson worth keeping:** never mutate xterm from outside the write
stream. `write()` is a queue; `reset()`, `clear()` and friends are not.
Anything that must be ordered with respect to the bytes has to travel
*as* bytes.

## 2026-08-08 (superseded) — the burst failure has a mechanism: resync thrash

> **Superseded by the entry above.** The router-side reading here was
> wrong: nothing was dropped, and the "contiguous hole" was a viewport
> that had stopped following output. The resync *thrash* below is real and
> still worth tuning — replaying a grown ring to an FE that is behind
> because it is saturated is poor medicine — but it is a performance
> smell, not the failure. Kept for the log.

Third occurrence (CI run 31243202190, the agentd-fix push). Got the
router.log out of the trace artifact this time, and it names the shape:

```
channel 6: FE behind — suppressing live output until resync
channel 6: resync complete ( 40712 bytes replayed)
channel 6: FE behind — suppressing live output until resync
channel 6: resync complete ( 95312 bytes replayed)
… behind → resync ×7, replay growing 40K → 95K → 146K → 226K → 328K → 355K
channel 6: resync complete (277170 bytes replayed)   ← last line, then silence
```

**The recovery is feeding the problem.** The FE falls behind, so the
router suppresses live output and replays the WHOLE ring; the ring has
grown (GrowFor fires while `behind`) so each replay is bigger than the
last; a 300KB replay is itself more than the FE can absorb, so it is
behind again the moment it lands. Seven rounds in ~1.5s, each replaying
more than the one before.

What lands in the DOM matches: the viewport sits at burst line ~19113 of
20000 **with the shell prompt after it** — so the tail of the burst
(19114…20000 and `-END`) was dropped, while output produced *later* (the
prompt) was forwarded live. A contiguous mid-stream hole with the stream
healthy on both sides of it.

Not yet pinned: exactly which window drops that hole.
`resyncChannel` holds `b.shellMu` across snapshot → send → `behind=false`,
and the forward path takes the same lock, so the obvious
snapshot-vs-clear gap should not exist. Candidates: the credit-gated
suppression path dropping without re-arming `behind`; or FE-side loss
under a 300KB replay. Reproducing needs the CPU squeeze from the
2026-08-06 entry (2 cores + hogs), not a healthy dev box.

Worth noting the fix direction regardless of where the hole is: replaying
a grown ring to an FE that is behind *because it is saturated* is the
wrong medicine. A resync should replay a bounded tail, or back off.

## 2026-08-08 — `term-wedge-recovery` burst soak: NEW signature (CLOSED)

> **Closed** by the `term.reset()` / write-queue fix at the top of this
> file. The guesses in this entry ("frames dropped between router and
> xterm", "no resync replayed it") were wrong in an instructive way: the
> reading assumed that content missing from the DOM was content that never
> arrived. For a terminal, the DOM is the *viewport* — absence there says
> nothing about the buffer. That is what cost two entries and three CI runs.

**Seen during:** CI on the 0.13.1 main-branch run (the tag run failed on
agent-fs instead — same tree). One red out of 477.

Distinct from the 2026-08-06 vacuous-barrier entry (that fix held: the
marker discipline worked). This time the terminal genuinely dropped the
tail: rendered rows froze at burst line **~18003 of 20000** for the full
45s barrier — 88 identical polls — while the **shell prompt, which comes
after the burst, did render**. So frames between ~18003 and the prompt
(including `${tag}-END`) were dropped between router and xterm and nothing
repaired the gap. END should have been well inside the 256KB scrollback
ring, so the open question is why no resync replayed it: either the channel
never got marked `behind` for the drop window, or the watchdog resync never
fired. That is the live-path cousin of the reattach-path fix that merged in
`term-close-19` (c06a2d5). Not reproduced locally yet; one occurrence.
Next hit: pull the router.log attachment and look for `behind`/resync lines
around the freeze.

---

## 2026-08-06 — `term-wedge-recovery` burst soak: the barrier was vacuous

**Seen during:** CI on **PR #20** (`outstanding-agent-app-fixes`), the first
branch to run the gate after the three fixes below landed. One red spec out
of 459 — which is the point: with three specs permanently red, this would
have been waved through as the usual noise.

```
1 failed  tests/term-wedge-recovery.spec.ts:52 › heavy output burst drains
          without hanging the terminal
22 skipped, 436 passed (8.2m)
```

**Verdict: pre-existing, NOT the PR.** Under a deliberately CI-like squeeze
(pinned to 2 cores + a busy-loop hog on each, 2 workers, 10 repeats) both
trees fail at the same rate:

| Tree | Result |
|---|---|
| PR #20 (`edb3e14`) | 1 failed / 19 passed |
| main (`962d57e`), same stress | **1 failed / 19 passed** |

The PR touches `shell_session.go` and the terminal FE, so it looked like a
prime suspect; it isn't.

**Mechanism.** The drain barrier asserted `toContainText(`${tag}-END`)` —
but `${tag}-END` is *inside the command the test types*, and the shell
echoes that command line back. `toContainText` reads xterm's **rendered
rows**, so whether the barrier matches the echo or the real output is a race
with the burst scrolling that line out of the viewport. Both outcomes are
wrong, and we saw both:

- **CI:** the echo was still on screen → the barrier passed with zero lines
  drained → the interactivity probe ran into a terminal still flooding and
  timed out at 10s. The captured DOM shows the viewport at burst line ~340
  of 20000 — the terminal was healthy, just busy.
- **Local, under contention:** the echo had already scrolled off → the
  barrier waited for the genuine marker and blew its 30s budget with the
  viewport at line ~621.

**Fix:** split the marker in the shell (`echo ${tag}-E''ND`) so the typed
line and the output are textually distinct, and apply the same trick to the
post-burst probe so it proves the shell *ran* the command rather than that
the keystrokes echoed. Drain budget 30s → 45s (and the test 60s → 90s):
20000 lines is ~8x the credit window and the FE has to render them, so this
is the slowest barrier in the suite on a loaded small runner — sized clear
of the observed worst case, not tuned to the median.

**Worth keeping in mind:** every `toContainText` against a terminal has this
hazard. Asserting on a string the test itself typed proves nothing about
what the shell did.

---

## 2026-08-06 — all three standing failures: root-caused and fixed

**Tree:** `branches/e2e-flakes` off main `350654b`. Host: buzz, 32 cores,
8 workers, `WASH_E2E_MULTICALL=1`. This closes the entry below (reconnect)
and the 2026-07-29 entry (display tier) — both were real product/harness
bugs, neither was load.

### 1. `reconnect` — the boot splash was eating the click

Not a bind failure and not a failed re-dial, which were the two hypotheses:
the replacement router came up fine and the shell reconnected on its own
every time. What timed out was the **click on "Reconnect now"**. Playwright
said so all along, in the call log rather than the error line:

```
- element is visible, enabled and stable
- <div role="status" id="wash-boot" aria-live="polite">…</div> intercepts pointer events
```

`#wash-boot` is the boot splash: `position:fixed; inset:0; z-index:999999`,
opaque, and only `pointer-events:none` once it's marked done. It's torn
down by `wash:desktop-painted` from the session app — so if the connection
drops *before* the desktop has painted, that signal never comes and the
splash sits on top of the connection banner until its 12s backstop fires.
The spec kills the router as soon as `wash-app-session` is visible, which
is exactly that window (instrumented: splash still live at t=0.15s, gone by
t=2.15s). By the time the splash cleared, the backoff loop had already
reconnected and the button was detached — so the click waited out the full
30s for a button that would never come back.

A **real bug, not a test artifact**: during any outage that starts before
first paint, a user sees an opaque "starting wash…" splash instead of the
banner, and cannot click the retry button underneath it.

Fixed in `web/shell/src/main.tsx`: `'reconnecting'` joins `'closed'` /
`'unauthenticated'` in the boot-step effect that fails the ws step and
tears the splash down, and the banner's `z-index` moves above the splash so
no future full-screen overlay can swallow the one piece of chrome an outage
makes essential.

**A/B, both directions** (`reconnect.spec.ts`, multicall layout, verified by
bundle byte-size which build was loaded):

| Shell bundle | Result |
|---|---|
| unfixed (73346 B) | fails in 5s at the new trial-click, naming `#wash-boot` as the interceptor |
| fixed (73367 B) | 10/10 and 20/20 green, whole spec ~1s |

The spec now asserts *reachability* while the port is still dead
(`retry.click({ trial: true })` — the actionability chain including
hit-target, no click), which is what actually regressed; and it tolerates
the backoff loop beating the click to the recovery, but only if the banner
really did clear.

### 2 & 3. `display-term-xclock` / `display-guest` — the A10 stale-match race

The 2026-07-29 entry had the mechanism right and the fix shape right; it
just never landed. Confirmed here: the compositor publishes env **more than
once** (geometry early, socket names once Xwayland is up), `waitForLog`
scans from t=0, so the specs' `env.publish from` wait was satisfied by the
*earlier* publish and the terminal was launched before `spawnEnv` carried
`WASH_X_DISPLAY` — `xclock` then exits with `Can't open display:` and no
window ever maps.

Fixed by making the fact assertable rather than the event:
`internal/router/app_session.go` now logs `keys=…` (sorted, comma-joined)
on the env.publish line, and the four display specs wait for
`env.publish from .*keys=.*WASH_X_DISPLAY`. Guarded by a new
`TestEnvPublishLogsKeys` so the line a spec greps can't be quietly reworded.
`waitForLog` also gained the A10 cursor (`from` + `logCursor()`) for the
next spec that needs "after this point" rather than "ever".

**A/B, `playwright test display` (13 specs, whole tier in parallel):**

| Tree | 5 runs |
|---|---|
| before | 4 red / 1 green — 21.5s, 21.5s, 21.5s, **8.2s**, 27.9s |
| after | **5 green** — 9.5s, 9.0s, 8.4s, 8.4s, 9.0s |

Note the wall-clock: every red run sat in the 21–27s band the 2026-07-29
entry identified as the signature, and every green one in the 8–9s band.

**The "compositor stall" was never a stall.** That entry's open question —
*why does the compositor stall when several run concurrently* — rested on
that 21s-vs-8s split. The 21s is just the spec's own 20s `waitFor` for a
window that a dead `xclock` was never going to map. Nothing stalls;
concurrency only widens the window the race needs. Also picked up the C5
prompt barrier for the same four specs (xterm mounted ≠ shell ready) since
they type into a pty; it is not what was failing.

**Verdict:** all three fixed, none quarantined. `make push`'s e2e gate can
gate again.

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
