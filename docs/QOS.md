# wash — QoS & flow control (draft)

Status: draft for Phase 6a. Subsumes the deferred
"raw discipline + credit backpressure" item in [WIRE.md §15](WIRE.md). Read
that first for frame format and channel model — this doc only adds the QoS
layer on top.

## 1. Problem

The router fans every app's traffic onto **one** browser ↔ router byte pipe
(WS in production, virtio-console in the v86 demo). Without scheduling:

- A chatty app (`cat 100MB` in wash-term, 50k-entry listing in wash-fm)
  saturates the pipe and head-of-line-blocks every other app's frames,
  including keystrokes the user just typed in a different window.
- Transport-level flow control (TCP RWND under WS, virtqueue full for
  virtio-console) is blunt: when it kicks in, *all* of wash freezes,
  because the transport can't see logical streams.

We fix this above the transport, at the wash protocol layer, so the same
mechanism applies unchanged whether the carrier is WS or virtio-console.

## 2. Non-goals

- Per-app fairness within a class. Defer; revisit if it bites.
- Bandwidth shaping in absolute byte/s terms. Classes + credits are enough.
- DSCP-style packet marking for routing decisions. We have one pipe.

## 3. Priority classes

Two classes for v1, room for four. Verb-keyed classifier inside the router;
apps never declare a class.

| Class       | Bits | Examples                                                          |
|-------------|------|-------------------------------------------------------------------|
| Interactive | 00   | keystrokes, mouse, focus, app-lifecycle, window geom, `app_msg` from FE |
| Bulk        | 01   | `pty.output`, `fs.list_reply`, `fs.read_reply`, watch events, log lines |
| _Reserved_  | 10   | (future: Background — telemetry, lazy prefetch)                   |
| _Reserved_  | 11   | (future: Control — credit/keepalive, must never block)            |

Encoding: two bits in the frame header, taken from currently-reserved flag
bits **1–2** of the existing 1-byte `flags` field (see
[WIRE.md §2](WIRE.md)). No new field; no header growth.

```
 0 1 2 3 4 5 6 7
+-+-+-+-+-+-+-+-+
|E|CLS|R R R R R|     E   = END (existing)
+-+-+-+-+-+-+-+-+     CLS = class (bits 1..2)
                       R  = reserved, MUST be 0
```

Senders set CLS; receivers MAY ignore. v0.0 senders writing 00 are
implicitly Interactive — safe because the classifier defaults to
Interactive for unknown verbs (under-prioritization is corrected by
operator action, not silent starvation).

## 4. Router-side scheduler

```
  apps ────unix socket────┐
                          ▼
                ┌───────────────────┐
                │  classifier       │   verb → class
                │  (per-frame)      │
                └────────┬──────────┘
                         │
       ┌─────────────────┼─────────────────┐
       ▼                 ▼                 ▼
   queue[Inter]      queue[Bulk]      queue[Ctrl]   bounded ring buffers
       │                 │                 │
       └───────── strict priority drain ───┘
                         │
                         ▼
                  transport writer
                  (WS or virtio-console)
```

- One bounded queue per class. Sizes (initial guess, tune in profiling):
  Interactive **256 frames**, Bulk **64 frames**, Control **64 frames**.
- Drain order is strict priority: Control → Interactive → Bulk. Strict
  priority is safe because Interactive volume is naturally tiny (mouse,
  keys, window events); it cannot starve Bulk in practice.
- When a class queue is full, the router **stops reading the producing
  app's Unix socket**. Kernel SO_RCVBUF backpressure does the work — the
  app's `Write` on its fd blocks, no protocol involvement.
- Selection is per-class, not per-app. Per-app fairness within Bulk is a
  Phase 7 concern.
- **The kernel's send buffer is FIFO and outside the scheduler.** Strict
  priority only orders frames still in the class queues; a frame handed
  to the socket waits in the kernel's send buffer behind everything
  already there, and Linux autotunes that buffer up to `tcp_wmem` max
  (4 MB). On a link the router can outrun — a VPN, a far host — that is
  where bytes actually queue, and a control frame submitted after a bulk
  burst waits seconds behind it while the scheduler, never blocked,
  thinks priority is being honoured. So the shell socket's send buffer
  is pinned small (`SO_SNDBUF`, 256 KB by default, `WASH_SHELL_SNDBUF`
  overrides; `internal/router/shell_sndbuf.go`): the writer blocks early,
  the drain order applies, and a control frame's worst-case wait is one
  buffer. The throughput ceiling is buffer/RTT, which is above what such
  links carry. `drainLoop` logs a control write that blocks >250 ms with
  the queue depths, so the condition is visible by mechanism.
- **Bulk frames are kept small at their producers** for the same reason:
  a control frame waits at least for the frame ahead of it on the wire.
  Bundles chunk at 32 KB; agentd streams a reply as coalesced deltas
  (the text added per ~50 ms, `apps/agentd/be/transcript_emit.go`)
  rather than re-sending the accumulated message on every chunk, which
  was quadratic bytes per reply.

## 5. Credit-based FE → router flow control

Transport-level backpressure (TCP / virtqueue) tells the router "stop
sending" but says nothing about *which channel* is slow. Per-channel credits
let the FE pace individual streams — e.g. a slow wash-fm renderer pauses fm
without affecting term.

### 5.1 Credit frame

Control-channel JSON message on channel 0 of the **browser ↔ router**
connection (see [WIRE.md §8](WIRE.md)):

```json
{ "t": "channel.credit", "ch": 17, "n": 65536 }
```

- `ch` — channel id receiving the grant. MUST be a live channel.
- `n`  — additional bytes the FE can absorb on `ch`. Cumulative. Unsigned
  32-bit.

Errors (per [WIRE.md §13](WIRE.md)):

- `unknown_channel` — `ch` is not open.
- `credit_overflow` — outstanding credit on `ch` would exceed 2^31. Sender
  is buggy or malicious; router MAY close the channel.

### 5.2 Initial window

Each channel opens with an implicit initial window:

| Discipline | Initial credit | Notes                                            |
|------------|----------------|--------------------------------------------------|
| JSON       | unlimited      | Control. Tiny. Bypasses credit accounting.       |
| CBOR       | 256 KiB        | App event channels. Tune per profiling.          |
| Raw        | 64 KiB         | Set explicitly at OPEN; receiver may override.   |

### 5.3 Router state per channel

```
  sent_bytes        // bytes already pushed toward FE on ch
  granted_bytes     // sum of initial + all credit{ch,n} received
  // invariant: sent_bytes <= granted_bytes
```

When `sent_bytes == granted_bytes` for `ch`, the router stops dequeuing
frames destined for that channel from its priority queues. The frame stays
queued in its class queue; other channels on the same class continue to
drain. If the class queue then fills, kernel backpressure stops the app.

This composes: a slow renderer doesn't block its class; a stuck class
doesn't block the transport (other classes still drain); a stuck transport
doesn't lose data (kernel backpressure all the way to the app's `Write`).

### 5.4 Direction

v1 ships **FE → router** credits only. App → router credits are unnecessary
because the router is fast (Go, native, in-process queues); router → app
credits are unnecessary because Unix socket SO_SNDBUF + the app's read
loop handle it. Add the symmetric direction if profiling shows otherwise.

## 6. Classifier table (initial)

Source of truth lives in `cmd/wash-router/qos.go`. This table is the seed.

| Verb (CBOR `t` field)         | Class       |
|-------------------------------|-------------|
| `key.down`, `key.up`, `key.press` | Interactive |
| `pointer.*`                       | Interactive |
| `focus.*`, `window.*`             | Interactive |
| `app_msg` (FE → app)              | Interactive |
| `app_msg` (app → FE)              | Bulk *      |
| `pty.output`                      | Bulk        |
| `pty.input`                       | Interactive |
| `fs.list_reply`, `fs.read_reply`  | Bulk        |
| `fs.watch_event`                  | Bulk        |
| `log.*`, `metric.*`               | Bulk        |
| `channel.credit`, `ping`, `pong`  | Control     |
| _unknown_                         | Interactive (default-safe) |

\* `app_msg` from app→FE is heterogeneous (a status update vs. a content
blob). Phase 7 may let the OPEN handshake declare a per-channel class
override; for now, classify as Bulk and accept the conservative case.

## 7. Backpressure interactions

End-to-end story for one bulk flow (`cat 100MB` in wash-term):

```
wash-term ──unix──▶ router ──queue[Bulk]──▶ ws ──▶ FE term renderer
   ▲                  │                              │
   │ Write blocks     │ stops reading                │ slow render
   │ (SO_RCVBUF)      │ wash-term fd                 │
   │                  │ when q[Bulk] is full         │
   └────────── backpressure chain ◀──────────────────┘
                                  ▲
                                  │ credit{ch,n} on term's channel
                                  │ when render absorbs bytes
```

End-to-end story for a concurrent interactive flow (keystroke in wash-fm):

```
shell input ──ws──▶ router (channel.input) ──queue[Inter]──▶ unix ──▶ wash-fm
```

Keystroke frame jumps the queue ahead of any Bulk frames already waiting.
Wash-term's flow doesn't block it; wash-fm's slow render doesn't affect it
(input flows the other direction, no credit gate).

## 8. Implementation footprint

Roughly (router):

- `qos.go` — class enum, classifier table, per-class queues (`chan
  *Frame` with bounded capacity). ~100 LoC.
- `scheduler.go` — strict-priority drain loop in the transport writer
  goroutine. ~50 LoC.
- `credit.go` — per-channel credit state + handler for `channel.credit`
  on the shell control channel. ~80 LoC.
- Wire: 2 bits added to existing `flags` byte (no header growth) + one
  new control verb (`channel.credit`). Update [WIRE.md §2](WIRE.md) flag
  table and §8 control-message list.

App side: **no change**. Classifier is router-internal.

FE side: only the renderer needs to emit `channel.credit` as it absorbs
bytes on raw/Bulk channels. ~30 LoC in the channel-receive plumbing.

## 9. Test plan

- **Unit**: classifier table coverage; credit accounting under overflow;
  scheduler picks Control before Interactive before Bulk.
- **Integration**: two-channel test where one channel drains slowly (no
  credit refill) — verify the other channel keeps making progress.
- **Soak**: `cat 1GB` in wash-term while wash-top updates at 1Hz and
  wash-fm types in a path field. Measure keystroke p99 latency — exit
  criterion is **<50ms p99 under sustained Bulk saturation**, both for
  the interactive flow inside the same app (wash-term keys) and across
  apps (wash-fm keys).
- **Demo**: same soak in v86 over virtio-console; same target, slack
  for emulation overhead → **<150ms p99**.

## 10. App SDK API

The router classifies on the 2 class bits in the frame `flags` byte (§3).
Those bits get set when the SDK serializes a frame; app code never touches
the wire. Two emit methods + two handle variants, mirrored on both SDKs:

**Go SDK** (`internal/sdk/bus.go`):

```go
bus.Emit("fs.watch_event", evt)      // default → Interactive
bus.EmitBulk("pty.output", chunk)    // → Bulk

sdk.Handle(b, "list", handler)       // reply inherits caller's class
sdk.HandleBulk(b, "list", handler)   // reply always Bulk
```

**JS / TS SDK** (mirror):

```ts
bus.emit("fs.watch_event", evt);
bus.emitBulk("pty.output", chunk);

bus.handle("list", handler);         // reply inherits caller's class
bus.handleBulk("list", handler);     // reply always Bulk
```

Rules:

- `Emit` / `emit` always Interactive — fits the common case (small RPC calls,
  UI events, status updates).
- `EmitBulk` / `emitBulk` is the only thing an app author needs to remember
  when streaming pty output, large reply structs, or file content.
- Request → reply class propagation is automatic via the call context (the
  handler emits its reply with the caller's class unless `HandleBulk`
  overrides).
- Raw channels (e.g. terminal byte stream) default to Bulk at OPEN time;
  override via the OPEN options (`sdk.OpenRaw(ctx, win, sdk.Interactive)` —
  rare case).

**Built-in classifier (SDK-internal, not API):** a small map of known verb
suffixes — `*.output`, `*.list_reply`, `*.read_reply`, `*.watch_event`,
`*.stream` — defaults to Bulk so apps that follow naming conventions get
correct behavior without remembering `EmitBulk`. Explicit `EmitBulk` always
wins; the table is a default fix-up, not policy. ~15 lines, one place to
edit when conventions change.

## 11. Open questions

- **Class declaration at OPEN**: should an app be allowed to ask for
  Interactive on a channel that the classifier would default to Bulk?
  Probably yes (e.g. a future low-latency audio app), gated by capability.
  Out of scope for 6a.
- **Credit on the app↔router socket**: if a misbehaving app floods the
  router faster than the router can classify+enqueue, the kernel handles
  it via SO_RCVBUF, but we lose visibility. Add a per-app rate limit /
  drop policy in Phase 7 if observed.
- **Frame fragmentation**: WIRE.md disallows fragmentation in v0.0
  (END=1 always). If we ever allow it, the class bits must be identical
  across fragments — document there, not here.

## 12. Link-health counters, as built

`internal/router/linkstats.go` accumulates one connection's counters —
per-class tx bytes/frames, queue-full blocks, drops, queue high-water,
credit stalls, rx totals — with atomics on seams the egress path already
has. They ride `link.stats` (~1/s) and the About window's **Link** section
renders them; each finished connection is folded into session-lifetime
totals so the figures survive a reconnect.

### 12.1 Per-app split

The class table says how the link is being used; `LinkStatsSnapshot.Apps`
says by whom. Attribution happens where the producing app is known, which
is two places and only two:

- the drain loop's raw-channel path — pty output, bundles, thumbnails,
  video — where the channel binding names the app, and where a lookup was
  already being done for the display counters;
- the `app_msg` relay, for control-channel envelopes. This is the last
  point that knows: by the time the frame reaches the wire, a control
  frame carries no app identity at all.

So the rows are a PARTIAL view of the class totals by construction.
Router-originated lifecycle traffic — window create, session patches, the
link push itself — has no app to bill and is deliberately not invented.
The About panel renders the difference as one derived row (`router
(lifecycle)`) rather than leaving a table that visibly does not add up.

Two consequences worth knowing:

- The two seams sample at slightly different moments (an app's bytes when
  the envelope is relayed, the class total when the frame is written), so
  a frame in flight at snapshot time can briefly make an app's share
  exceed the total. The renderer clamps the remainder at zero.
- App bytes are the app's own payload, not the framing around it, so an
  app author reading the column sees the number they can act on.

This is the panel to reach for when one app is suspected of crowding a
class — it is how the roster-push flood was confirmed to be agentd's
state pushes rather than the transcript stream (§10's Bulk suffixes had
already moved the transcript).

### 12.2 Frame size is part of the priority contract

The scheduler is preemptive BETWEEN frames and not inside one: the drain
loop commits a whole frame to the transport before it consults the queues
again. So the largest frame a lane emits is the worst-case delay that lane
can impose on every lane above it, and priority cannot undo it.

`writeChunked` caps every router-side chunked emitter at `maxChunkBytes`
(32 KB) for that reason. Before it, panel bundles emitted 256 KB — one
whole clamped send buffer — and scrollback replay emitted the entire ring
in a single frame, up to `ChannelScrollbackMaxBytes` (4 MiB). Both sat on
the Interactive lane, so a reattach or a settings-panel load stalled
pointer motion for as long as the transfer took.

A new emitter that writes more than a few KB belongs behind
`writeChunked`, whatever lane it uses. The sndbuf clamp (shell_sndbuf.go)
is the companion rule and bounds a different thing: what the KERNEL may
queue ahead of a control frame, not what one frame costs to write.

### 12.3 Class is priority, not ordering

Frames of the same class are FIFO, so it is tempting to use a shared
class to keep a transaction ordered. Two flows did, and both paid for it
by dragging bulk-sized data up into the latency lane.

The rule instead: a transactional flow announces its byte count in its
bind (`channel.bind` Size, `panel.read.ok` size) and the shell completes
on that count, never on the Unbind. Then the data can ride whatever lane
its size deserves and a control frame overtaking it cannot truncate
anything. `assets.ts` and `panels.ts` both work this way.

A flow that genuinely cannot announce its length must keep all of its
frames in ONE lane — but it should be a low one.

### 12.4 Moving traffic down a lane moves its dependencies too

Two faults, one mistake, both caught by the browser suite and neither by
unit tests. Recorded because the lane taxonomy invites exactly this.

**A terminator must ride its data's lane.** Panel data moved to Bulk with
its Unbind left on Interactive, so the Unbind overtook the bytes it
terminates (§ docs/TEST_FLAKES.md already calls unbind-overtakes-payload
expected behaviour) and the shell tore the transfer down before a byte
arrived. Every settings panel stopped mounting. Byte-count completion
removes the truncation hazard, not this one — the receiver must also not
treat an early terminator as the end.

**Credit keys on CLASS, not on the flow.** So moving the reattach replay
to Bulk silently gave it a 64 KB window it had never had; it blocked in
Reserve while holding shellMu and the whole reattach stalled. Recovery
replays are creditless by nature — resyncChannel already knew this. Until
credit is keyed on the binding instead, ANY write promoted into Bulk
inherits a flow-control window, and that is a property to check before
moving something down.
