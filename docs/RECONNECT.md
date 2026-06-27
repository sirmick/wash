# Connection robustness & disconnect diagnostics

How wash keeps a browser shell attached across network blips and
laptop-suspend, and what it shows the user when it can't. Companion to
docs/PTY_ROBUST.md (which covers the *channel/terminal* side of a
reconnect — head ownership, scrollback replay, resync); this doc covers
the *transport* side — detecting a dead socket and recovering it.

## The problem: the laptop-suspend zombie

A clean disconnect is easy: the OS sends a TCP FIN, the browser fires
`WebSocket.onclose`, and the FE reconnect loop (`web/shell/src/ws.ts`)
re-dials with backoff. The router tears the shell session down, keeps the
apps alive, and the next connection reattaches every terminal with its
scrollback (PTY_ROBUST).

The hard case is **suspend**. Close the lid and the OS freezes the socket
mid-connection — frequently with *no* FIN. JS timers freeze too. On wake:

- `WebSocket.readyState` often still reads `OPEN`. `onclose` never fired.
- So the FE never enters `reconnecting`; the banner still says connected.
- Neither side was pinging (the browser WebSocket API exposes no
  ping/pong to JS, and the router ran no ping ticker), so nothing notices.
- The router's per-write deadline (PTY_ROBUST Fix C, 30s) only bites when
  the router is *writing*; an idle terminal writes nothing.

Net: a zombie connection that looks alive. The first keystroke after wake
vanishes or hangs. This is the "I closed my laptop and wash got stuck"
report.

## The fix: an application-level heartbeat

Channel 0 carries a `ping`/`pong` pair (WIRE.md §8). It is app-level, not
WebSocket-protocol ping, for two reasons: the browser API won't let JS
send protocol pings, and an app-level beat works identically over the
virtio/v86 transport.

**FE (`web/shell/src/ws.ts`, `Conn`):**

- While `open`, send `{t:"ping",seq}` every `HEARTBEAT_INTERVAL_MS` (15s),
  Control class so it jumps ahead of bulk in the send queue.
- Expect a `pong` with the same `seq` within `PONG_TIMEOUT_MS` (10s). A
  miss ⇒ the socket is a zombie ⇒ `forceRedial()`: null the dead socket's
  handlers (so its late `onclose` can't double-drive the state machine),
  close it, and dial again immediately — never trusting the frozen socket
  to ever report its own death.
- Any inbound frame (not just pong) refreshes `lastContactAt`, so a busy
  link is never mistaken for a dead one.
- Heartbeat runs only on real-WebSocket transports. A relay
  (`RelayChannelSocket`) rides the local WS, which heartbeats itself; the
  virtio demo has no suspend story. Gated on the same flag as the
  `/auth/check` preflight.

**Router (`internal/router/shell_session.go`):**

- `handlePing` echoes `pong` (Control class), echoing `seq` verbatim and
  holding no per-ping state.
- `dispatch` stamps `lastReadAt` on every inbound frame.
- `readIdleLoop` reaps a connection silent for `readIdleTimeout` (45s, =
  3 missed pings): it closes the transport, which unblocks the read loop
  and runs the normal teardown — freeing the session and, crucially,
  logging the disconnect instead of pinning a dead `ShellSession` forever.
- **Backward-compat guard:** the watchdog only arms after the first ping
  (`sawPing`). An older FE that predates the heartbeat never pings, so it
  is never falsely reaped during an idle stretch — it keeps the legacy
  "detected only when written to" behaviour.

## Waking faster than the next tick

Waiting up to a 15s interval (or a 10s pong deadline) after resume is
sluggish. The shell listens for the browser's resume signals and hands
them to `Conn.wake(source)`:

- `visibilitychange` → visible
- `online`
- `pageshow` (covers bfcache restores, where the socket is certainly
  stale)

`wake()` does an immediate, tight-deadline (`WAKE_PROBE_TIMEOUT_MS`, 4s)
liveness probe when `open`, or skips the backoff and re-dials now when
already down.

## Diagnostics when disconnected

Previously the banner showed one word ("disconnected"). Now, while down:

- **Banner** (`web/shell/src/main.tsx`, `ConnectionBanner`): state, live
  "no contact for Ns", reconnect attempt count, `navigator.onLine` ("device
  offline" vs "router unreachable"), a **Reconnect now** button that skips
  the backoff (`Conn.reconnectNow()`), and a lost-input warning when the
  offline send-queue overflows (the user's recent keystrokes were dropped).
- **Event trail** (`Conn.onEvent`): every lifecycle transition
  (connect / close+code / reconnect-scheduled / zombie / wake /
  reconnect-now / lost-input) is forwarded to `shellLog`, so the *why* of
  a drop lands in the router log and the About panel — the data you want
  when chasing a flaky-link report.
- **`window.__washDiag().conn`** (`Conn.diag()`): a devtools snapshot —
  state, ms since last contact / last pong, last RTT, attempts, total
  reconnects, pending queue depth, last close code/reason, online flag.

## Remote (peer) windows across a reconnect

Remote-app relay channels (docs/REMOTE.md) do **not** survive a local
reconnect the way local terminals do: the router closes every `ssh -L`'d
peer socket when the shell session ends (`closeAllPeers`), because a peer
channel carries no router-side replayable state (A is a payload-opaque
conduit; the recoverable state lives in B's router) and its pump is pinned
to one shell by invariant. It also can't tell the FE over the by-then-dead
socket.

So on a **reconnect** (an `open` after we had been down), the shell scrubs
the stale peer `RouterClient`s and re-issues `peer.attach` for each
attached origin over the fresh connection (`reattachPeersAfterReconnect`).
`handlePeerAttach` is idempotent router-side and the host's peer
registration outlives the blip (the `com.wash.remote` supervisor app
does), so remote windows self-heal instead of going dead. Recovery is
heavier than for local terminals (re-dial + B re-snapshots its desktop;
no scrollback survives on the remote side because A never had it).

## Constants

| Constant | Where | Value | Meaning |
|----------|-------|-------|---------|
| `HEARTBEAT_INTERVAL_MS` | ws.ts | 15s | FE ping cadence |
| `PONG_TIMEOUT_MS` | ws.ts | 10s | periodic-beat pong deadline |
| `WAKE_PROBE_TIMEOUT_MS` | ws.ts | 4s | wake-probe pong deadline |
| `readIdleTimeout` | transport.go | 45s | router reaps a silent armed conn |
| `wsWriteTimeout` | transport.go | 30s | per-write deadline (PTY_ROBUST Fix C) |

## Tests

- `web/shell/src/ws.test.ts` — heartbeat ping/pong + RTT, pong consumed
  (never reaches the app handler), no-pong → zombie force-redial, wake
  probe + wake-redial, reconnectNow no-op-while-open, lost-input event,
  diag fields.
- `internal/router/heartbeat_test.go` — `handlePing` echo + class + arming,
  `dispatch` liveness stamp, `idleReapDue` decision (incl. the legacy-FE
  guard).
- `internal/wire/msgs_test.go` — ping/pong ctrl round-trip.
