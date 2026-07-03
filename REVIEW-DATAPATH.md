# Data-Path & QoS Correctness Review

Scope: wire framing (`internal/wire`), router data plane (`internal/router`), PTY plumbing
(`internal/pty`, `internal/sdk`), and the FE side of the path (`web/shell/src`,
`web/lib/src/terminal.tsx`, `web/fs-client`). Method: full read of the wire/router/sdk/pty
sources, byte-level trace of PTY output → xterm, keystroke → PTY, and upload → BE, cross-checked
against the existing unit tests (credit/qos/ringbuf/wedge/fuzz) and e2e specs. Findings are
code-trace based; nothing was executed.

## Executive summary

The primitives are in good shape: the frame codec, ring buffer, strict-priority scheduler and
credit ledger are individually correct and well tested. The serious problems are all at the
*seams* between those primitives:

1. **The production PTY stream never uses the Bulk/credit path at all** — a class-bits mismatch
   in the Go SDK means terminal output rides ClassInteractive, so the entire PTY_ROBUST Fix-B
   machinery (credit gating, behind/suppress, resync) is inert for terminals and only reachable
   by wash-display video.
2. **A channel that goes "behind" can stay dark forever** — recovery is only triggered by an
   inbound per-channel credit grant, but a suppressed channel delivers no bytes, so the FE
   (which grants only on absorption) may never send one. Terminals have a partial input-triggered
   nudge; video has none.
3. **A WS-blip reconnect duplicates up to 256 KiB of scrollback into a still-mounted terminal** —
   the reattach replay is not preceded by a `channel.resync`, and the FE appends it to the
   existing xterm buffer.
4. Several **head-of-line blocking chains on the single shell read loop** (asset streaming,
   blocking writes into app sockets, SDK channel-queue backpressure) can freeze all inbound
   traffic and even false-trip the read-idle reaper.

---

## Resolution status (2026-07-02, merged to local main)

The fix pass (branches fix-reliability-a/b, fix-qos-class) closed every HIGH/
MEDIUM finding here. Commit hashes are the local-main merge commits.

| # | Status | Where |
| --- | --- | --- |
| **F1** PTY output is ClassInteractive | ✅ Fixed | `f31621d` (C1): `sdk.OpenChannelBulk`; pty opens Bulk. Per-caller audit in the commit. |
| **F2** behind channel can never resync | ✅ Fixed | `638df86` (A3): per-shell behind-watchdog (`behindWatchdogLoop`). Same as REVIEW-RECONNECT M1. |
| **F3** reattach duplicates scrollback | ✅ Fixed | `b5cf1c1` (A1): `channel.resync` before replay. Same as RECONNECT H1. |
| **F4** resync overtakes queued Bulk | ✅ Fixed | `024da1d` (A4): resync reset+snapshot ride ClassBulk (FIFO). |
| **F5** read-loop head-of-line blocking | ✅ Fixed | `36ef6c9`/`2810606`/`6ad3e7f` (B1): liveness stamp + peer/app write deadlines. Input-stall half closed by `8bb9635` (G1): asset streaming moved off the dispatch loop onto its own goroutine (`streamAssetChunks`). |
| **F6** SDK deliver blocks the app read loop | ✅ Mitigated | The whole-desktop freeze chain is broken by B1's app-write deadline (`6ad3e7f`): a wedged app is torn down instead of blocking dispatch. The suggested drop-oldest in `RawChannel.deliver` was not needed and not done. |
| **F7** pendingRaw unbounded + credits unconsumed bytes | ✅ Fixed | `8ecbe0a` (C2): `pendingRaw` capped (drop-oldest); credit granted only on real consumption. |
| **F8** credit-ledger drift | ✅ Fixed | `8ecbe0a` (C2): FE grants only for Bulk-class frames a subscriber consumed. |
| **F9** ReadLoop reader-goroutine leak | ✅ Fixed | `80f8fbb` (E1): the buffered send selects against a `done` channel closed on return; unit test asserts the reader exits at teardown. |
| **F10** max-size frame kills the peer relay | ✅ Fixed | `99b53ab` (E2): oversized frames split across relay frames — pieces sent at ClassControl so the strict-priority scheduler can't interleave them (FE `send` mirrors the split). Chose split over capping B producers. |
| **F11** minor notes (Reserve wakeup, DecodeFrameRaw fuzz, TrySubmit-after-Close) | ✅ Fixed | `c2f975e` (E3): `FuzzDecodeFrame` cross-checks `DecodeFrameRaw`; `TrySubmit`/`SubmitTelemetry` no-op after `Close`; `Reserve` single-producer assumption documented. |

"What looks solid" below was verified and left unchanged.

---

## Findings (by severity)

*All findings addressed — see the resolution-status table above. (Full detail in git history.)*

---

## What looks solid (verified)

- **Frame codec** (`wire/frame.go`): length checked against the 16 MiB cap before allocation,
  reserved-bit/END validation on both decode paths, big-endian 24-bit channel packing symmetric
  with the TS codec (`web/shell/src/wire.ts`); pinned by `frame_test.go` + three fuzzers.
- **StreamTransport write locking** (`wire/transport.go:29-55`): the header+payload two-write
  sequence is mutex-serialized, so concurrent SDK writers can't interleave frames.
- **Ring buffer** (`router/ringbuf.go`): wrap arithmetic (including the `len(p) ≥ cap` tail-copy
  case) is correct; `realignReplay`'s UTF-8/CSI trimming only runs on truncated snapshots;
  covered by `ringbuf_test.go` and `spawn_ringbuf_test.go` (io.MultiWriter contract).
- **Scheduler** (`router/qos.go`): strict-priority fast path, FIFO-within-class (no re-queue on
  the slow path — correctly reasoned in the comment), Close-unblocks-Submit/Next; all pinned by
  `qos_test.go` / `qos_integration_test.go`.
- **Credit ledger** (`router/credit.go`): Reserve/TryReserve/Refund/Grant arithmetic (clamping,
  overflow check, buffered-1 wakeup that can't lose a grant with a single waiter) is correct;
  `credit_test.go` covers cancellation, close, and concurrent grant/reserve.
- **Forwarder vs resync atomicity**: ring append + `shell`/`behind` sampling share one `shellMu`
  critical section (`app_session.go:230-234`), and `resyncChannel` holds `shellMu` across
  snapshot + enqueue — no byte can land between a resync snapshot and resumed live stream, and
  the single-forwarder-per-channel invariant closes the sample-then-enqueue window.
- **Head ownership** (Fix A): head sampled before `shellMu` (documented lock order), adoption +
  background-drop semantics pinned by `wedge_repro_test.go` and `shell_head_test.go`.
- **drainLoop teardown** (Fix C): failed FE write closes the scheduler, unblocking all producers
  (`TestWedge_SlowClientHeadOfLine`); `wsWriteTimeout` converts a dead-but-open client into that
  failure.
- **Spawn pid race**: `pendingMu` held across `Spawn`+slot-insert (`router.go:855-875`)
  eliminates the fast-child fresh-attach misroute.
- **fm upload FE backpressure**: HWM/LWM pacing on `bufferedAmount` + periodic macrotask yield +
  cancel polling (`apps/fm/fe/src/main.tsx:155-192,1556-1577`) keeps a cancel frame from queuing
  behind megabytes — the historical head-of-line bug stays fixed.
- **Peer relay invariants**: pump reads `b.shell` lock-free under a real happens-before
  (pinned at creation; reattach explicitly skips live peer bindings), class-preserving verbatim
  splice via `DecodeFrameRaw`, creditless by design with B doing end-to-end flow control.
- **SCM_RIGHTS handoff** (`unix_listener.go`): exactly-one-fd enforcement with close-on-error on
  every path, replay-prefix conn, bounded header read; peer-cred gate before any fd handling.
- **base64/[]byte pitfall**: no violations found in app BE→FE structured payloads; the repos'
  own comments (`apps/disks/be/types.go:5`, `app_session.go:612-616`) enforce the rule at the
  places it previously bit.
- **SDK raw delivery aliasing**: `dispatch.go:50-52` copies the payload before handing it to the
  channel queue (and `DecodeFrame` allocates per frame), so no buffer-reuse corruption.
