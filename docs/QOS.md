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
