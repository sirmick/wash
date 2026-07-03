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
| **F5** read-loop head-of-line blocking | ⚠️ Partial | `36ef6c9`/`2810606`/`6ad3e7f` (B1): liveness stamp at the read + peer/app write deadlines. **Asset streaming still runs inline on dispatch** — deferred, `TODO(review F5)` in `shell_session.go` + TODO.md. |
| **F6** SDK deliver blocks the app read loop | ✅ Mitigated | The whole-desktop freeze chain is broken by B1's app-write deadline (`6ad3e7f`): a wedged app is torn down instead of blocking dispatch. The suggested drop-oldest in `RawChannel.deliver` was not needed and not done. |
| **F7** pendingRaw unbounded + credits unconsumed bytes | ✅ Fixed | `8ecbe0a` (C2): `pendingRaw` capped (drop-oldest); credit granted only on real consumption. |
| **F8** credit-ledger drift | ✅ Fixed | `8ecbe0a` (C2): FE grants only for Bulk-class frames a subscriber consumed. |
| **F9** ReadLoop reader-goroutine leak | ⏸ Deferred | Out of scope — TODO.md. |
| **F10** max-size frame kills the peer relay | ⏸ Deferred | Out of scope — TODO.md. |
| **F11** minor notes (Reserve wakeup, DecodeFrameRaw fuzz, TrySubmit-after-Close) | ⏸ Deferred | Out of scope — TODO.md. |

"What looks solid" below was verified and left unchanged.

---

## Findings (by severity)

### F1 — HIGH (CONFIRMED by code trace): PTY/terminal output is ClassInteractive, so the credit + behind/resync path is dead code for terminals

- `internal/sdk/channel.go:21-25` — `RawChannel.writeClass` zero value is **ClassInteractive**;
  it is only ever set to Bulk for `ChannelKindFile` (`channel.go:196-198`).
- `internal/pty/pty.go:257-296` — the PTY session opens a **generic** channel
  (`conn.OpenChannel`) and pumps output via `io.Copy(ch, f)` → `RawChannel.Write`
  (`channel.go:89-104`) → frames stamped `WithClass(Interactive)`.
- `internal/router/app_session.go:252` — the non-blocking suppress path is gated on
  `class == wire.ClassBulk && b.credit != nil`; Interactive frames fall through to the
  blocking, credit-free `sh.WriteRawFrameClass(..., Interactive)` (`app_session.go:266`).
- `docs/QOS.md` line 37/161/267 says `pty.output` is Bulk and "Raw channels (e.g. terminal byte
  stream) default to Bulk at OPEN time" — the code disagrees.

Consequences:

- Every Fix-B protection (`tryWriteRawBulk`, `behind`, `resyncChannel`, the FE stall-nudge) is
  **unreachable for real terminals**. The wedge unit tests (`wedge_repro_test.go:217,321`) pass
  only because they hand-craft `.WithClass(wire.ClassBulk)` frames the production terminal never
  emits; the e2e soak (`term-wedge-recovery.spec.ts`) asserts recovery properties that hold
  without Fix B.
- A `cat 100MB` flood shares the **Interactive** queue (256 slots, `qos.go:38`) with window
  patches, app_msg deliveries, clipboard broadcasts etc. — the strict-priority isolation QOS.md
  promises ("wash-fm's slow render doesn't affect term keys") is defeated; a slow FE makes the
  pty flood block *other* apps' Interactive producers via `Submit`.
- The FE still grants credit for these bytes (`web/shell/src/main.tsx:596` calls
  `credit.absorbed` for every raw delivery), which the router never debits — the per-channel
  ledger drifts open-ended (see F8).

Concrete scenario: browser tab is alive but its renderer is slow (heavy page, throttled tab).
`yes` runs in wash-term. Interactive queue fills; the term app's read goroutine blocks in
`Scheduler.Submit(context.Background(), …)` (`shell_session.go:876`) → back-pressures into the
child (arguably fine), but every *other* Interactive producer on that connection (session
patches, focus events, other apps' `app_msg.deliver`) blocks behind it too. Only a fully dead
client (30s `wsWriteTimeout`) breaks the loop.

Fix direction: make generic raw channels write Bulk by default (per QOS.md §"OPEN time"), or add
an explicit Bulk open for pty/thumbs channels; then F2/F4 below become the live behavior of the
terminal path and must be fixed with it. Add an integration test that drives a real
`pty.Session` and asserts the wire class of its output frames.

### F2 — HIGH (CONFIRMED for video, latent for terminals): a "behind" channel can never resync — the only trigger is a credit grant that a suppressed channel stops generating

- `resyncChannel` (`internal/router/router.go:1448`) is called from exactly one place:
  `handleChannelCredit` (`shell_session.go:567`). The comment "the next credit grant **or the
  watchdog** retries" (`router.go:1447`) refers to a router-side watchdog that does not exist.
- The FE grants credit only when it **absorbs bytes** on that channel and only after ≥32 KiB
  accumulates (`web/shell/src/credit.ts:37-49`). Once the router suppresses live output
  (`app_session.go:256-264`), the FE receives nothing on the channel, its residual counter
  (< 32 KiB) never crosses the threshold, and no grant — hence no resync — is ever sent.
- Race variant even when a grant is due: the forwarder fails `tryWriteRawBulk`, and a grant that
  raced in *before* `b.behind = true` (`app_session.go:259-261`) finds `behind == false` in
  `resyncChannel` and no-ops; afterwards there is nothing left to trigger recovery.
- Mitigations that exist: (a) terminals have an FE stall watchdog, but it only fires after the
  user **types** into the stalled terminal (`web/lib/src/terminal.tsx:698-714` — gated on
  `awaitingOutput`); a passive `tail -f` window freezes indefinitely. (b) `wash-display` video —
  today the *only* production Bulk+credited stream (see F1; `cpp-sdk/wash/wire_conn.cpp:70-72`
  classes video frames Bulk, and `registerChannel` gives video channels a credit ledger,
  `router.go:666-672`) — has **no nudge at all** (`wash-app-display.ts` never subscribes to
  resync or nudges), so a display window that goes behind (e.g. the shared Bulk queue filled by
  an fm download while the video window happened to be at its credit edge with no grant in
  flight) freezes until reconnect.

Fix direction: add a router-side retry — e.g. `readIdleLoop`-style ticker or a timer armed when
`behind` is set that re-runs `resyncChannel` — and/or have the FE emit a keepalive zero-grant for
channels it has subscribers on.

### F3 — MEDIUM-HIGH (CONFIRMED by code trace): reattach replay duplicates scrollback into a live terminal on a same-page reconnect

- On every new shell connection, `reattachChannelsToShell` (`internal/router/router.go:1371-1433`)
  migrates each channel to the new head and sends `ShellChannelBind` + the **entire ring
  snapshot** (up to 256 KiB) — with **no `channel.resync`** preceding it.
- On a page *reload* this is correct: elements remount fresh and the replay lands in an empty
  xterm via the `pendingRaw` queue. But on a **WS-blip reconnect** (`ws.ts` reconnects in place;
  `applySessionSnapshot` in `wm.ts:296` upserts existing windows without remounting), the
  terminal element and its `subscribeRaw` callback survive — the replay is appended by
  `writeOrBuffer` → `term.write` (`terminal.tsx:440-449`) to an xterm that already displays those
  same bytes.
- Result: after every transient disconnect (suspend/resume, router restart, network blip) each
  open terminal shows its recent scrollback twice, mid-screen state garbled until the next clear.
  The reconnect e2e (`e2e/tests/reconnect.spec.ts`) has no terminal open;
  `term-reattach.spec.ts` uses `page.reload()` — the blip path is untested.

Fix direction: have `reattachChannelsToShell` send `wire.NewShellChannelResync(...)` before the
replay (the FE handler `deliverResync` is a no-op when no subscriber exists, so fresh mounts are
unaffected), or track FE-side whether a channel already has rendered content and reset on rebind.

### F4 — MEDIUM (CONFIRMED sequence, currently only reachable on video): resync reset + snapshot overtake same-channel Bulk frames still queued in the scheduler

- `resyncChannel` emits the reset at **ClassControl** (`tryWriteCtrl`, `shell_session.go:917-927`)
  and the snapshot at **ClassInteractive** (`tryWriteRawInteractive`), while frames of the same
  channel accepted *before* the wedge may still sit in the **Bulk** queue (credit is debited at
  enqueue time, not drain time). Strict priority (`qos.go:179-203`) drains Control/Interactive
  first, so the FE can observe: `…older bulk…, reset, snapshot(ring incl. those bytes), STALE
  bulk frames (duplicates), live bulk`.
- Failing sequence: slow link; 60 KiB of channel frames queued in Bulk undrained; producer hits
  the credit edge → `behind`; the FE (which absorbed earlier bytes) sends a grant →
  `handleChannelCredit` → resync jumps the queue → the 60 KiB stale frames drain *after* the
  snapshot and are re-fed to the consumer.
- Today only wash-display video is Bulk+credited (F1), where per-frame framing makes this a
  transient glitch (each raw frame is parsed independently, `wash-app-display.ts` header docs).
  The moment pty output is reclassified Bulk (the F1 fix), this becomes visible terminal
  corruption after every wedge recovery.

Fix direction: defer resync until the scheduler holds no queued frames for that channel (track a
per-channel in-queue count), or emit reset+snapshot at ClassBulk so they stay FIFO behind the
stale tail (the deferral logic in `resyncChannel` already tolerates TrySubmit failure).

### F5 — MEDIUM (CONFIRMED chains): shell read-loop head-of-line blocking — one slow consumer stalls all inbound traffic, and can false-trip the read-idle reaper

The shell's `dispatch` (`shell_session.go:330`) runs synchronously on the single read loop, and
several handlers can block indefinitely:

- **Asset/panel streaming in-line**: `handleAssetRead` (`shell_session.go:435-497`) loops the
  whole file through blocking `Submit` calls (Background queue = 64×64 KiB ≈ 4 MiB of headroom).
  On a slow link, a multi-MB wallpaper/font keeps `dispatch` inside one call for
  `(size − 4 MiB)/link-rate` seconds. During that time **no inbound frame is read** — no
  keystrokes, no credit grants, no pings — and `lastReadAtNanos` (`shell_session.go:337`) goes
  stale. If the stall exceeds `readIdleTimeout` (90 s, `transport.go:47`), `readIdleLoop` reaps a
  *live* connection mid-transfer; below 90 s it still freezes the desktop's input.
- **Blocking write into the app socket**: raw FE→app frames are forwarded via
  `b.app.writeRawFrame` (`shell_session.go:382` → `app_session.go:1008-1018`), a blocking
  `unix`-socket write under `inst.writeMu` with no deadline. An app BE that stops reading (see
  F6) fills SO_SNDBUF and wedges the whole shell read loop. The FE upload backpressure
  (`apps/fm/fe/src/main.tsx:161-192`, HWM 1 MiB) bounds fm's exposure but not other producers'.
- Same-loop hazards, smaller: `handleWindowFocus`/`handleWindowResize`/clipboard broadcast all do
  blocking `inst.WriteEvt` fan-out from `dispatch`.

Fix direction: run asset/panel streams on their own goroutine (they are already
transaction-framed); give `writeRawFrame`-to-app a write deadline or per-app egress queue; exempt
"dispatch is busy, not dead" from the idle reaper (e.g. stamp `lastReadAtNanos` before *and*
after `dispatch`).

### F6 — MEDIUM (CONFIRMED chain): SDK `RawChannel.deliver` blocks the app read loop → whole-desktop input freeze through F5

- `internal/sdk/dispatch.go:52` → `RawChannel.deliver` (`channel.go:121-126`) blocks when the
  64-slot queue is full. The consumer for a terminal channel is `io.Copy(f, ch)` into the PTY
  master (`pty.go:306-315`) — which itself blocks when the pty's kernel buffer is full because
  the foreground child stopped reading stdin.
- Chain: stuck child (stopped/`Ctrl-S`-style state) + sustained paste/input → pty buffer full →
  `io.Copy` blocked → `deliver` blocks the SDK read goroutine → app's unix-socket RCVBUF fills →
  router's `writeRawFrame` blocks → **shell read loop wedged** (F5) → every terminal and app on
  that browser connection stops accepting input; credit grants stall too, so all output paths
  degrade. Recovery only when the child drains or the pty is killed.
- Failure needs ~64 frames + SO_RCVBUF + 64 KiB pty buffer of input (< 1 MiB) — reachable by
  pasting a large buffer into a stopped program.

Fix direction: make `deliver` drop-oldest or grow-bounded for pty-type channels (input loss to a
non-reading child is kernel behavior anyway), or decouple the router→app write from the shell
read loop (per-app writer goroutine with a bounded queue).

### F7 — LOW-MEDIUM (CONFIRMED): FE `pendingRaw` buffers unboundedly and still grants credit for bytes nobody consumes

- `web/shell/src/api.ts:105-110,192-205` — bytes arriving on a channel with no subscriber are
  queued in `pendingRaw` with no size cap, and `main.tsx:596` still calls `credit.absorbed` for
  them, so the router keeps the stream flowing.
- Scenario: an app whose bundle fails to import (element never defined → window never mounts →
  no `subscribeRaw`) while its BE streams on a raw channel — e.g. a reattached terminal for an
  app whose re-declared bundle throws, or a video channel for a window the element failed to
  mount. Browser memory grows for as long as the BE writes.

Fix direction: cap `pendingRaw` per channel (drop-oldest with a log), and only grant credit from
a real subscriber's consumption.

### F8 — LOW (CONFIRMED): credit-ledger drift — grants for bytes never debited

- The FE grants for **all** raw bytes it absorbs (`main.tsx:596`), including Interactive-class
  traffic that bypassed `Reserve` (reattach replays up to 256 KiB, resync snapshots, all
  terminal output per F1). The router-side window (`granted − sent`) therefore inflates
  permanently — each reconnect adds up to +256 KiB per channel. Not a stall or loss, but it
  progressively weakens the credit backstop for the channels that do use it (video), and it makes
  `link.stats` credit numbers misleading. `Grant` overflow (`credit.go:139-156`) is unreachable
  in practice (2^64).

Fix direction: only call `absorbed` for Bulk-class frames (the FE has `classOf(flags)` available
in `ws.ts` but discards the class before `onRaw`).

### F9 — LOW (CONFIRMED): `wire.ReadLoop` can leak its reader goroutine at teardown

- `internal/wire/readloop.go:26-35`: the reader goroutine sends results into a 1-buffered
  channel. If `ReadLoop` returns (handler error / ctx cancel) while one result is already
  buffered, the goroutine's *next* send blocks forever; closing the transport unblocks
  `ReadFrame` but not the pending send. One goroutine + one frame pinned per affected session
  teardown. Bounded, but it's the loop under every app and shell session.

Fix direction: `select` the send against a `done` channel closed by `ReadLoop` on return.

### F10 — LOW (CONFIRMED edge): a maximum-size frame kills the peer relay

- The relay wraps B's whole frame (8-byte header + payload) as the payload of one A-frame:
  `pumpPeerToShell` (`peer.go:163-187`) and `RelayChannelSocket.send`
  (`web/shell/src/relay-socket.ts:58-64`). A legal 16 MiB B-frame produces a 16 MiB + 8 byte
  payload > `MaxPayload` (`frame.go:36`) → `EncodeFrame`/`encodeFrame` fails → the pump breaks
  and the relay channel is torn down (`peer.go:186`). All real producers chunk at ≤256 KiB
  today, so this is latent; it will bite whichever future stream first emits a near-cap frame
  over the relay.

Fix direction: split oversized relay payloads across multiple raw frames (the splice is a byte
conduit; B's deframer reassembles), or cap B-side producers at `MaxPayload − 8`.

### F11 — NOTE: minor gaps, kept for the record

- `ChannelCredit.Reserve` wakes one waiter per grant (`credit.go:82-101`); with multiple
  concurrent reservers on one channel a small-`n` waiter can stay parked while the woken
  large-`n` waiter re-sleeps consuming the token. All current channels are single-producer, so
  this is only a latent constraint — worth a comment on the type.
- `DecodeFrameRaw` (`frame.go:216`) is not covered by `FuzzDecodeFrame`; its validation
  duplicates `DecodeFrame` by hand, so a future edit can desync them silently.
- `Scheduler.TrySubmit`/`SubmitTelemetry` still enqueue after `Close()` (no closed check on the
  fast path) — frames land in a queue no drainer will read. Harmless today (only reached during
  teardown) but surprising.
- `handleAssetRead`'s `chunkSize` frames are sliced from the shared asset cache — safe because
  `assetEntry` bytes are immutable by construction; flagged only because the copy-on-write rule
  for snapshot-outliving slices was a past race source.

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
