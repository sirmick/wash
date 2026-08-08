# Session prompt — the e2e failures that outlive every branch

> **DONE (2026-08-06, branch `e2e-flakes`).** All three fixed, none
> quarantined; `make e2e-test` is green and `make push` gates again. Both were
> real bugs, not load: `reconnect` was the boot splash sitting on top of the
> connection banner and eating the "Reconnect now" click (a user-visible fault,
> not a test artifact), and the display pair was the A10 stale-`env.publish`
> match launching terminals before `WASH_X_DISPLAY` was published. The
> "compositor stalls under parallel load" hypothesis in §2 below was wrong —
> see `docs/FLAKE_LOG.md` 2026-08-06 for the mechanisms, the A/B tables, and
> that dead end. Kept for the method (baseline-before-blame, the harness traps).

Paste this as the opening prompt for a dedicated session. It is self-contained.

---

You are fixing the **standing e2e failures** in the wash repo: the specs that go
red on branches which did not touch them, and which every author since has
A/B'd, shrugged at, and merged past. `docs/FLAKE_LOG.md` is the dated record of
each sighting; this prompt is the work order.

They matter more than their count suggests. `make push` — the project's own
CI-equivalent gate — runs `e2e-test` and refuses to push if anything is red. So
these three failures mean **the gate cannot pass**, and every release since has
been pushed by running the gate's steps by hand and deciding the red was
somebody else's. That is a gate that has stopped gating.

## What is failing, and what is known

As of **2026-08-06**, a full `make e2e-test` on a settled tree returns
**448 passed / 3 failed**.

### 1. `reconnect.spec.ts` › "router drops → banner + Reconnect-now → same-port restart recovers"

**The one to start with.** It is *deterministic* — 3/3 isolated runs — which
makes it far cheaper to chase than the two below, and it is the only one of the
three that has never been investigated.

- Fails with `Test timeout of 30000ms exceeded`, at the point where the spec
  clicks `[data-testid="wash-connection-retry"]` after starting a **replacement
  router on the same port** (`e2e/tests/reconnect.spec.ts:46-55`).
- A/B'd 2026-08-06 against baseline `1cba66a` in a fresh worktree: **fails there
  too**. Pre-existing, not introduced by the ACP work.
- Unknown, and the first thing to establish: does the second router fail to bind
  the port (SO_REUSEADDR not taking effect in time), or does it bind and the
  shell fail to re-dial? Those are different bugs with the same symptom. The
  fixture's `router.log()` for BOTH routers is the evidence; capture them
  separately rather than reading the merged attachment.
- Beware the harness, not just the product: the spec relies on the first
  router's listener being released promptly. A router that lingers, or a
  `startRouter` that races its own readiness line, would produce exactly this.

### 2. `display-term-xclock` and 3. `display-guest` (wayland clipboard)

The **display-tier standing finding**, first logged 2026-07-29. Both have been
seen failing on baseline commits that touched nothing related, and they move
around: which one fires depends on parallel load, and a third display spec is
usually clean in the same run.

- `docs/FLAKE_LOG.md` 2026-07-29 has the A/B table — M1 vs baseline, three runs
  each, different specs red each time.
- These need a real compositor and are load-sensitive, so they are *harder* than
  #1 and should not be attempted first.
- Hypothesis worth testing before anything else: the tier is being run with more
  parallelism than a single Xwayland/compositor per box can serve. Check whether
  serialising just the display project makes them green — if it does, the fix is
  a worker constraint, not a product change.

## The method this repo expects

From `docs/FLAKE_LOG.md`, and it is not optional:

> before blaming (or absolving) your branch, get a baseline. Rebuild the tree at
> the pre-change commit and run the same subset the same number of times. A
> single green baseline run proves nothing about a flake that fires one run in
> three.

Concretely: `git worktree add --detach /tmp/wash-baseline <commit>`, build it,
`pnpm install --ignore-workspace` in its `e2e/` (it is not a workspace member),
and run the *same spec* the same number of times. Record the result in
`FLAKE_LOG.md` whether it confirms or refutes your theory — the refutations are
worth as much as the confirmations, and this file exists because nobody wrote
them down the first three times.

Useful facts about the harness:

- Run the suite from `e2e/`, or via `make e2e-test`. Invoking `npx playwright`
  from the repo root resolves a different config and fails confusingly.
- **The suite owns the tree while it runs.** Rebuilding anything mid-run
  (`make multicall`, a binary target) makes tests fail against a half-written
  tree — observed 2026-08-06 as 93 spurious failures.
- `make e2e-test` runs the **multicall** layout, which is what ships. A spec can
  pass standalone and fail there; if it does, suspect something that differs
  between a probed app and a linked-in one (see the `registry.App.Assets` bug in
  `apps/ai/be/app.go`'s comment — the failure was completely silent).

## What done looks like

1. `reconnect` passes, deterministically, ten runs in a row — or is proven to be
   testing something the product does not actually promise, and is rewritten to
   test what it does.
2. The display pair is either fixed, or **quarantined deliberately**: marked
   with a reason and excluded from the gate, so that `make push` can pass and
   start meaning something again. A known-red spec inside a blocking gate is
   worse than an honestly-skipped one.
3. `make push` passes end to end without a human deciding which failures to
   ignore.
4. `FLAKE_LOG.md` gains an entry for whatever you learned, including the dead
   ends.

## What NOT to do

- Do not add retries to make them green. The suite dropped retries deliberately
  (see `[[wash e2e load flakes]]` — the "load flakes" turned out to be a lost
  worker cap plus build gaps, and retries would have hidden that).
- Do not delete a spec because it is inconvenient. `reconnect` covers the
  laptop-suspend recovery path that PTY_ROBUST and the reconnect work exist to
  protect; it is one of the more valuable specs in the suite.
- Do not fix it only for the standalone layout.
