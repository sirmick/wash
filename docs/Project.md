# wash — design and status

The current design record and what is in flight. Per-feature designs that
outgrow a section here get their own doc and are linked from it; this file
stays the index and the status.

Backlog: [Todo.md](Todo.md) · sweeps ledger: [Sweeps.md](Sweeps.md) ·
latest sweep: [Review-findings.md](Review-findings.md).

---

## What wash is

A Linux-style desktop environment served to a browser. A Go **router** owns
windows, focus, app lifecycle and the wire protocol; **apps** are separate
processes (Go backend + SolidJS frontend) that attach to it over a unix
socket; the **shell** is the browser-side chrome that renders what the
router says exists. AI is a first-class service rather than a bolt-on —
`com.wash.agentd` hosts agent sessions, `com.wash.ai` is a window onto
them.

Deeper: [Architecture](ARCHITECTURE.md) (structure),
[Internals](INTERNALS.md) (wire protocol), [Agent app](AGENT_APP.md)
(the agent contract).

## Build and run

- `./build.sh` — everything (`make wash`: FE bundles, embed, multicall
  binary + `wash-<app>` symlinks in `out/`). `./build.sh test` — unit
  tier. `./build.sh e2e` — full browser suite. It is a thin wrapper over
  the Makefile, which is the real build.
- Targeted browser runs need `make test-app` first: the specs put
  `out/e2e` on PATH for the fake agent adapter, and without it a spec
  silently reaches for the real `codex` on the host. `make e2e-test`
  builds it; raw `npx playwright test` does not.
- `./run.sh` — build, then start a dev router on `0.0.0.0:11000`.

Test layers: Go unit (`go test ./...`) → FE pure kernels (`node --test
--conditions=browser`) → components (vitest, `*.ctest.tsx`) → full browser
e2e (Playwright, `e2e/tests/*.spec.ts`). `make unit-test` is the first
three; `make e2e-test` the last; `make all-test` everything including the
KVM VM gates. `make push` is the CI-equivalent gate and pushes only if it
passes.

## In flight

### Agent UX — messenger consolidation (0.15)

Design: [AGENT_MESSENGER.md](AGENT_MESSENGER.md). Predecessor:
[AGENT_UX.md](AGENT_UX.md) (phase Now, shipped in 0.14.0).

**Status:** M5 done and released in 0.14.1 — one status vocabulary in
`web/lib/src/agent-status.ts`, which fixed three live defects (a failed
session rendered green; the rail counted `stale` and `done` rows as
working; wash-term could not express `stale`).

**Open:** M1 the list merge (live + stored sessions, one row, one search —
reconcile the three time representations FIRST, as its own commit), M2 one
window per host, M3 compose-in-place, M4 the taskbar pill count. M2 and M3
ship together or M2 ships a dead button.

**Open question:** whether one-window-per-host survives contact with real
use. The always-open sessions list has been in daily use since 0.14.0;
that feedback should land before M2 is written.

### Default prompt — DONE, shipped in 0.14.2

Standing instructions sent to every new agent session ahead of what you
type. Stored as plain text at
`$XDG_CONFIG_HOME/wash/agent-default-prompt.txt`, beside the approval
policy and written the same way (temp file, `0600`, rename).

Applied in agentd's `agent_start` rather than the frontend, so it holds
however a session is started — the launcher, `wash ai --agent`, anything
that lands there. It goes first, separated by a blank line. **New sessions
only**: resuming or reattaching does not repeat it, and it does not ride
every message. This ACP version's `session/new` carries only cwd and MCP
servers, so there is no instructions field to use; it goes as the first
prompt, which also puts it in the transcript — an instruction the agent
was given should be one you can read back.

Editor is an Overlay reachable from the launcher (with a line saying
whether one is armed, because a prompt that silently prefixes every
session is the kind of magic that gets blamed on the agent) and from the
File menu. Only the flag rides the roster push; the text is fetched on
demand, following `agent_history`'s precedent.

The file is the truth, not the dialog. Plain text invites editing it in an
editor, so the flag is re-derived from the file on agentd's sweep
(`refreshDefaultPrompt`) rather than remembered from the last save —
otherwise a hand-written file and the launcher disagree until agentd
restarts. Found by hand-writing the file, which is how the repo's own
working rules were installed.

Tests: `apps/agentd/be/default_prompt_test.go` (store, clear, bounds,
composition, hand-edit reaching the flag, whitespace-only not counting),
`e2e/tests/agent-default-prompt.spec.ts` (the agent really receives it, it
survives a reload, clearing removes the file).

### Per-app traffic counters — DONE, shipped in 0.14.3

The About window's Link section now splits FE-bound traffic by the app
that produced it, per class, busiest first. Attribution lives at the two
seams that know the producer (the raw-channel drain and the app_msg
relay); everything else is router lifecycle traffic and is shown as a
derived remainder rather than counted, so the rows always reconcile with
the class totals above them. docs/QOS.md §12.1 has the shape and the two
caveats (sampling skew, payload-not-framing).

Tests: `internal/router/linkstats_test.go` (split, fold across
reconnects, snapshot is a copy), `apps/about/fe/src/app-traffic.test.ts`
(row order, the derived remainder, reconciliation, clamping),
`e2e/tests/about-app-traffic.spec.ts` (a terminal's real pty bytes are
attributed to the terminal, in the Bulk column).

### QoS lanes — IN FLIGHT

**Problem.** Window drag stutters even after the drag-jank work
(sndbuf clamp, geometry token, transcript deltas). The scheduler is
sound; the *taxonomy* is not. Everything a human waits on and everything
sizeable share one FIFO:

- geometry patches go through `WriteCtrl`, which sets no class, so they
  are Interactive by default (`router.go` broadcastPatches);
- bundle delivery is Interactive **on purpose**, at 256 KB per frame,
  so its Interactive Unbind cannot overtake the data and so it bypasses
  the credit gate (`shell_session.go` handlePanelRead);
- app messages default to Interactive (`classifyKind`);
- `link.stats` telemetry rides ClassControl — the *highest* lane — once
  a second.

Strict priority does nothing inside a class, and a frame is written
whole: there is no preemption point inside one. With a 256 KB sndbuf
clamp and 256 KB Interactive frames, a single bundle chunk is one full
send buffer of head-of-line blocking that the scheduler cannot help.

**Root cause.** Class is doing two jobs — priority *and* ordering — and
a third by accident: it also gates credit (`class == ClassBulk`). Any
sequence that must stay ordered has to live in one class, which is what
pushed bulk-sized data up into the latency lane.

**The lanes, stated once.**

| Class | What belongs there |
|---|---|
| Control | protocol liveness only: ping/pong, error envelopes |
| Interactive | what a human is waiting on NOW: input echo, geometry, focus, window lifecycle |
| Bulk | anything sized: bundles, assets, pty output, transcripts, transfers |
| Background | best-effort periodic: telemetry, thumbnails |

**Four changes.**

1. **Chunk smaller.** One `writeChunked` helper at 32 KB replaces three
   copies at 256/64/32 KB. Bounds worst-case head-of-line blocking to
   one small frame instead of one whole send buffer.
2. **Coalesce geometry.** A queued patch for a window is superseded by a
   newer one rather than both being delivered — X11's motion
   compression. Only bites when the writer is behind; zero added latency
   when it is not.
3. **Decouple credit from class.** The credit gate keys on the channel's
   ledger (`b.credit != nil`, the existing `noCredit` seam used by peer
   and file channels), not on the class bits. Bundle channels are bound
   creditless, which frees their data to ride Bulk; the Unbind rides
   Bulk too, so same-class FIFO keeps the transaction ordered — the
   precedent `closeChannel` already sets.
4. **Evict non-critical traffic.** Telemetry Control → Background.

**Test plan.** The gate this has always lacked: a latency-critical frame
must reach the wire promptly while a lower lane saturates. Scheduler
unit test for the overtake, a pure test for the coalescer's merge, a
router test that the bundle Unbind still lands after its data, and a
frame-size property test so the chunk cap cannot silently regress.

## Conventions this repo holds to

- **Both halves.** A test that crosses a process boundary asserts the
  frontend state *and* the backend — router log, filesystem, or the app's
  own echo. Either alone passes while the feature is broken.
- **Tiered gate.** Commit on build + unit green; push after the full
  suite; tag after packaging.
- **One version master.** Root `VERSION`; `make check-versions` fails the
  build if the Go default, deb changelog, rpm spec or APKBUILD drift.
- **Flakes are recorded, not shrugged at.** [FLAKE_LOG.md](FLAKE_LOG.md)
  carries the mechanism or an explicit "not root-caused".

## Known deviations from the working rules

Recorded rather than silently tolerated; each has a Todo entry.

- **Doc filenames are `UPPER_CASE.md`** (52 files), referenced from ~311
  files, many in Go and TS comments that would go stale silently. The
  rename wants its own change with a link check, not a drive-by.
- **`COMMANDS.md` sits at the repo root**, outside `docs/`.
- **`Sweeps.md` and `Review-findings.md` do not exist yet** — no sweep has
  been run under these rules.
