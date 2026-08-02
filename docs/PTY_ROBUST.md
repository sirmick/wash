# PTY / terminal robustness — design

Branch: `wash-pty-robust` (off `main`).

Goal, in one line: **a wash terminal must never hang.** It either keeps
working or visibly recovers — it never silently goes black, and it never
blocks the child shell.

This supersedes the A4 band-aids (`bcc18af` head-follows-foreground,
`7909bd2` head-adopts-stale-owned on `wash-connect-ux`) with a structural
model. Those narrowed the ownership race but left output-path wedges and the
"open socket but dead flow" case untouched.

## The governing principle

Terminal output is a **real-time stream**, not a reliable byte pipe. The
consumer is a human watching a screen, and the router already keeps a
per-channel scrollback ring (`ringbuf.go`) that is replayed on every
reattach. Therefore:

> When the FE can't keep up or is wedged, the correct behaviour is to **drop
> or coalesce output**, never to block the producer. A terminal that drops a
> burst and re-syncs from the ring is robust; a terminal that blocks the
> shell to preserve every byte is hung.

Every fix below follows from this: make the FE-bound path **lossy and
bounded**, and make ownership **authoritative** rather than a cached guess.

## Hang taxonomy (all confirmed in `main`)

| # | Wedge | Where | Direction | Today's behaviour |
|---|---|---|---|---|
| 1 | **Ownership race** — cached `b.shell` points at a stale/zombie shell | `shell_session.go:216` | FE→app (keystrokes) | keystrokes dropped → terminal dead to input |
| 2 | **Credit wedge** — `Reserve(context.Background())` never returns when FE stops granting and the channel isn't closed | `shell_session.go:696`, `credit.go:82` | app→FE (output) | router app-read goroutine blocks → back-pressures SDK queue → pty `io.Copy` → child shell stdout blocks |
| 3 | **drainLoop HOL** — one `ws.Write` with no deadline | `shell_session.go:731`, `transport.go` | app→FE | a slow/dead-but-open client blocks *every* terminal on that shell |
| 4 | **pty io.Copy back-pressure** — no timeout, mutual-deadlock window | `pty.go:120/130`, `sdk/channel.go` (queue=64) | both | inherits #2/#3; both copy goroutines can block with neither able to free the other |
| 5 | **No FE wedge detection** — FE only watches *socket close* | `web/shell/src/ws.ts:152` | — | logical stall under a healthy socket → silent black, no recovery trigger |

## The model

### Fix A — authoritative ownership (kills #1)

The head shell is the single authoritative driver of every non-peer terminal
channel. Replace "cache `b.shell`, drop on mismatch" with:

- A frame arriving from the **current head** always routes; the head adopts
  the channel if a stale owner held it. The head's input is **never dropped.**
- A non-head *background* shell's raw input is dropped (correct: it isn't the
  foreground driver), but this is now a positive rule keyed on `isHead()`, not
  an accident of a stale pointer.
- Peer (remote-relay) channels stay pinned to their pump — unchanged.

This generalises `7909bd2` from "adopt if orphaned **or** head" to "head is
authoritative, full stop," and removes the cached-pointer drop as a silent
failure mode.

### Fix B — non-blocking output via resync, never a torn stream (kills #2, #4-output)

The router's per-app read goroutine must **never block on FE credit** — but it
must also **never ship the FE a stream with a hole punched in it.** Dropping
mid-CSI or a mode-set (`\e[?1049h` alt-screen, `\e[?2004h` bracketed paste, an
SGR reset) is exactly how a terminal ends up stuck in the wrong mode. So
"lossy" is at the granularity of **resync**, not bytes:

- The scrollback **ring is always maintained byte-exact** — it is the
  authoritative recent history and is never dropped-into incrementally.
- The ring is **256 KiB while a shell is attached and keeping up, and grows
  to 4 MiB while nobody is taking delivery** (detached, or so far behind
  that forwarding stopped) — see `ChannelScrollbackMaxBytes`. Since the
  producer is never stalled to preserve output, buffering is the only lever
  there is; the buffer shrinks back to 256 KiB as soon as a shell has taken
  the history (reattach replay or resync), so idle tabs cost the base size,
  not the ceiling. Past the ceiling it goes back to overwriting the oldest
  bytes: bounded memory, never a blocked write. The FE's xterm keeps 20,000
  lines to match — at the old 1,000-line default it would have discarded
  most of a replay on arrival.
- The router's per-app read goroutine hands output to the ring + a small
  bounded live-queue and **returns immediately** — it never blocks on credit.
  `Reserve` loses `context.Background()`: cancellable on detach/rebind and
  bounded, so it can never strand the shared path or the child shell.
- A per-channel **sender goroutine** drains the live-queue to the FE under
  credit. While the FE is merely *behind* (within the 64 KiB window) the ring
  absorbs the burst and the FE catches up — **no drop at all.**
- Only when the FE is **declared wedged** (live-queue overflow / watchdog,
  Fix D) do we stop incremental delivery and mark the channel **desynced.**
  The next successful delivery is a single self-consistent **resync frame**,
  never a hole:

  > **resync = terminal reset + the B2-tracked DECSET/keypad modes re-seeded +
  > the `realignReplay`-trimmed ring snapshot.**

This is *exactly the reattach replay path* (B2 mode re-seed + `realignReplay`
torn-head trim + B3 reflow), promoted to a first-class operation the
wedge-recovery path can trigger without a full reconnect. The only thing ever
lost is scrollback older than the ring — already gone by design (B4). The FE's
xterm only ever sees byte-exact streams or clean, self-consistent resyncs.

### Fix C — drainLoop write deadline (kills #3)

Add a write deadline to the FE-bound `ws.Write`. A client that accepts TCP but
never reads (or a wedged tab) trips the deadline → the shell is treated as
gone → drainLoop exits → blocked producers unblock with `ErrSchedulerClosed`
→ the FE's normal reconnect path re-dials and reattaches. One slow client can
no longer hang other terminals on the shell.

### Fix D — watchdog + visible FE recovery (kills #5, "visibly recover")

- **BE:** per-channel last-progress timestamp. A channel with pending output
  the FE hasn't drained/granted for N seconds is flagged stale; the router
  stops waiting on it (force-drop to lossy) and may proactively drop the
  binding so a fresh attach re-seats it.
- **FE:** a per-terminal liveness check. If a terminal has unacked input or no
  output for N seconds under a *healthy* socket, surface a "terminal
  stalled — reconnecting…" affordance and trigger a rebind (`list_sessions`
  reconcile + ring replay). Instead of a silent black rectangle the user sees
  it recover.

## Scope — which terminals

The interactive terminals all ride **one shared core** (`internal/pty.Session`
→ router channel/dispatch/drainLoop → the shared FE component
`web/lib/src/terminal.tsx`). Fixing the core fixes them all with no per-app
work. A few terminal-shaped things are off that path and get an explicit call:

| Path | Plan |
|---|---|
| **term / edit / connect** — shared `internal/pty` + FE component | **Full A–D.** The whole real win, fixed once. |
| **packages** ("update package") — FE renders the shared terminal but BE streams apt/dnf output via `app_msg`, *no pty, no raw channel* | **Fix D only** (inherited FE recovery). No credit/ownership wedge applies; its detached-`app_msg` gap is A1, a separate ticket. |
| **priv `inline_pty.go`** — separate short-lived sudo/password pty via `onStream` callback | **Light hardening** — same ctx-cancel/timeout discipline. Cheap; "very robust" wants it. |
| **vscode `runInstallPTY` / login `driveSuPty`** — one-shot, already ctx-scoped, not interactive FE terminals | **Out of scope.** Recorded so they're not re-flagged. |
| **wash-remote** — a remote pty spliced verbatim through a `noCredit`, pump-pinned **peer channel** | **Transitive.** Host B's router makes B's terminal robust at the source once merged; on A's side the peer channel stays exempt from A/B (dumb byte splice) but gets C (drainLoop deadline) + D (FE recovery). |

## Milestones

- **M0 — deterministic repro harness. ✓** `internal/router/wedge_repro_test.go`
  — a named, failing-before-the-fix test for each wedge.
- **M1 — Fix A (authoritative ownership). ✓** Head shell owns every non-peer
  terminal channel; supersedes the A4 band-aids.
- **M2 — Fix B (non-blocking output + resync). ✓** The forward never blocks the
  child shell; a wedged channel is held byte-exact in the ring and recovered by
  a `channel.resync` (FE `term.reset()` + mode re-seed + realigned snapshot) on
  the next credit grant — no torn stream, no manual reload.
- **M4 — Fix C/D (watchdog + visible recovery). ✓** `wsWriteTimeout` bounds
  every FE-bound write, so a dead-but-open client can't pin the per-shell
  drainLoop and hang other terminals; on timeout the shell tears down and the
  FE's existing reconnect path re-dials and reattaches (visible recovery). The
  credit wedge recovers via M2's auto-resync.
- **M5 — e2e + soak.** `term-reattach.spec`, `remote-vm.spec`, a new
  `term-wedge-recovery.spec`, and a churn soak (rapid connect/disconnect +
  heavy output) — all green.

Deliberately deferred: a router-wide periodic *resync sweep* (force a resync on
a channel stuck `behind` even if the FE never sends another credit grant). The
credit-grant resync (M2) plus the write-timeout teardown (M4) cover every
realistic wedge — the only gap is an FE that keeps draining the socket but
never grants credit, which is an FE bug, not a transport state. Recorded here
so it isn't mistaken for an oversight.

## Open decision

Fix B changes terminal semantics: **output to a wedged FE is dropped and
healed from the ring on reattach, not buffered indefinitely.** This is the
right call for a real-time stream (and matches what xterm scrollback already
implies), but it is a deliberate semantic change worth confirming before
building M2.
