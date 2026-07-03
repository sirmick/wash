# Reconnect / Reliability Correctness Review

Scope: full reconnect lifecycle across `internal/router`, `internal/login`, `web/shell` + `web/lib`,
the SSH one-port relay, and child-process lifecycle. Recent fixes 718abe5 / fd9069c / 5523ef3 /
dd72d78 / 42d6698 scrutinized. Read-only review; every finding was traced (or refuted) at the code
level. Confidence: **CONFIRMED** = exact failing sequence traced end-to-end in code; **PLAUSIBLE** =
mechanism traced, trigger needs a specific (but realistic) condition.

## Executive summary

The PTY_ROBUST core (non-blocking forward, behind/resync, authoritative head, wsWriteTimeout,
idle-reap, offline send queue) is structurally sound and well-tested. The remaining holes cluster in
four places: (1) the **reattach replay is written into a terminal that is never reset** on a live
(same-page) reconnect — every suspend/resume duplicates scrollback; (2) the FE WS client can **dial
two sockets concurrently** after wake, losing queued input and flapping; (3) the shell's single
dispatch loop still performs **unbounded blocking writes** (peer socket, app unix sockets), so one
stuck endpoint freezes the whole desktop and then triggers a false idle-reap storm; (4) several
**"never comes back" paths**: dead `?s=` session URLs, crashed session/background apps that autoboot
never respawns, remote windows that don't re-attach after an SSH blip, and com.wash.remote leaking
ssh tunnels (plus host-B routers) on every router restart.

15 findings: 5 high, 7 medium, 3 low (plus a short list of smaller hardening notes).

---

## Resolution status (2026-07-02, merged to local main)

All 5 HIGH, the top 4 MEDIUM, and one LOW are fixed (branches
fix-reliability-a/b). Commit hashes are the local-main merge commits.

| # | Status | Where |
| --- | --- | --- |
| **H1** live reconnect duplicates scrollback | ✅ Fixed | `b5cf1c1` (A1): reattach sends `channel.resync` before the ring replay. |
| **H2** concurrent-dial race after wake | ✅ Fixed | `52bad9a` (A2): `dialing`/`tickInFlight` guards + `sock !== this.ws` bail in every handler. |
| **H3** dispatch-loop blocking writes | ✅ Fixed | `36ef6c9`/`2810606`/`6ad3e7f` (B1): liveness stamp at the read + peer & app-socket write deadlines. |
| **H4** dead `?s=` reconnect loop | ✅ Fixed | `57ccbb4` (B4): `handleRoot` validates `?s=` → redirects to `/sessions?err=session+ended`. |
| **H5** com.wash.remote leaks ssh tunnels | ✅ Fixed | `b0e64b9` (B2): `sdk.OnTerminate` cancels host ctxs + tears down mounts; `Pdeathsig` backstop. |
| **M1** behind terminal has no BE recovery | ✅ Fixed | `638df86` (A3): per-shell behind-watchdog. (== DATAPATH F2.) |
| **M2** crashed session app never respawns | ✅ Fixed | `00d9b3e` (B3): `tearDown` clears the started latch on unexpected exit. |
| **M3** crashed background service never respawns | ✅ Fixed | `00d9b3e` (B3): same, for `backgroundStarted`. |
| **M4** remote windows don't re-attach after SSH blip | ✅ Fixed | `d01d352` (B5): reconcile moved into the always-alive session FE; `peer.error` → toast. |
| **M5** deferred mountWhenReady ghost window | ✅ Fixed | `193f864` (F3): per-origin snapshot epoch + a per-record cancelled flag drop a superseded deferred upsert. |
| **M6** self-closed relay socket wedges a remote client | ✅ Fixed | `2d772a2` (F4): a fatal relay desync fires `onFatalClose` → `detachClient` instead of firing onclose (which redialed the dead socket forever). Composes with E2. |
| **M7** post-confirm close has no kill escalation | ✅ Fixed | `49608f6` (F1): `terminateWindowedApp` SIGTERM→SIGKILL after `windowCloseKillGrace`, mirroring `restartBackgroundApp`. |
| **L1** reattach migration ordering window | ✅ Fixed | `b5cf1c1` (A1): bind+resync+replay enqueued under `shellMu`. |
| **L2** queued input dropped on `unauthenticated`; silent head-steal | ⚠️ Partial | `bea9372` (F5): auth-loss queue clear now emits `lost-input`. Head-steal banner still deferred — needs a net-new BE→FE "opened elsewhere" message (`reattachChannelsToShell` silently reassigns ownership). |
| **L3** teardown gated on `cmd.Wait` | ✅ Fixed | `5285788` (F2): `cmd.WaitDelay` so Wait returns after the process exits even if a grandchild holds the stdout pipe. |
| Smaller notes | ⏸ Deferred | Out of scope — TODO.md. |

"What looks solid" and the test-gap list below were verified; the listed test
gaps 1–7 are now covered by the new unit/e2e tests added with each fix.

---

## HIGH

### H1. Live reconnect replays scrollback into a non-reset terminal → duplicated output every suspend/resume — CONFIRMED

- `internal/router/router.go:1366-1433` (`reattachChannelsToShell`), `web/shell/src/main.tsx:439-476`
  (`channel.bind` handler), `web/lib/src/terminal.tsx:547-555` (reset only on `channel.resync`),
  `apps/term/fe/src/main.tsx:414-416` (mounted tabs never remounted).
- On every new `HandleShell` the router migrates each channel to the new shell and sends
  `ShellChannelBind` + the **full ring snapshot** (up to 256 KiB). That is correct for a page
  refresh: the fresh terminal isn't mounted yet, bytes park in `pendingRaw` (`api.ts:199-204`) and
  render into a new xterm. But on a **live-page reconnect** (suspend/wake, network blip, idle-reap,
  the 718abe5-era reap storms) the page, the term app's tabs, and the `subscribeRaw` subscriptions
  all survive; `channel.bind` for a generic channel only does `channelOwner.set(...)` — **nothing
  resets the xterm** — so the replay is appended verbatim to a terminal that already displayed it.
  The FE resets only on `channel.resync` (`deliverResync` → `term.reset()` + mode re-seed), which the
  reattach path never sends. docs/PTY_ROBUST.md:79-82 claims resync "is exactly the reattach replay
  path" — the reset+mode-re-seed half is missing from reattach.
- Failure scenario: laptop suspends; router idle-reaps the zombie conn (90s); laptop wakes; ws.ts
  redials; `reattachChannelsToShell` replays every open terminal's ring → each terminal shows its
  last ~256 KiB duplicated, cursor/modes not re-seeded. Repeats on every blip.
- Coverage gap: `term-reattach.spec.ts` tests page **refresh** only; `reconnect.spec.ts` tests
  **router restart** (channels gone, no replay). The same-router live-reconnect case is untested.
- Fix: in `reattachChannelsToShell`, send `ShellChannelResync` (before the replay) instead of / in
  addition to the bare bind — a resync for an unsubscribed channel is an explicit no-op FE-side
  (`api.ts:235-236`), so the refresh path is unaffected while the live path gets the reset + mode
  re-seed. Add an e2e: type marker → drop WS without reload → reconnect → assert marker count == 1.

### H2. FE concurrent-dial race: `reconnectNow()`/`wake()` during an in-flight dial spawns a second socket; queued input lost, state machine flaps — CONFIRMED

- `web/shell/src/ws.ts:515-521` (`reconnectNow`), `:341-350` (`reconnectTick`), `:269-307`
  (`connect`), `:605-609` (`flushPending`).
- While a redial is in flight (between `reconnectTick()`'s `connect()` and that socket settling —
  can be 20s+ against a black-holed network) state is `'reconnecting'` with no timer armed.
  `reconnectNow()` only guards `open`/`connecting`/`closedByUser`, so a `wake('online')` or the
  banner's "Reconnect now" click calls `connect()` again and replaces `this.ws` **without detaching
  ws1's handlers**. If ws1 then opens, its `onopen` runs `flushPending()` against `this.ws` = ws2
  (still CONNECTING) → `InvalidStateError` after `clearPending()` → **every queued keystroke /
  save_state silently lost**, and the state can sit at `'reconnecting'` over a live-but-orphaned
  socket. Both sockets opening → two server-side attaches from one tab; the stale socket's `onclose`
  later re-enters `scheduleReconnect()` while the healthy one is up → self-sustaining flap with
  repeated head-steal + (per H1) replay duplication. This is the *normal* resume flow — `visible`,
  `online`, `pageshow` all fire around wake while the first redial is typically still in flight.
- `ws.test.ts:290-299` covers wake-while-backoff-armed, not wake-while-dial-in-flight.
- Fix: capture `const sock = this.ws` in `connect()` and bail in `onopen/onmessage/onclose` when the
  firing socket isn't current; make `reconnectNow()` a no-op (or teardown+redial) while a dial/tick
  is in flight; reentrancy flag around the `authGone()` await.

### H3. Shell dispatch loop performs unbounded blocking writes (peer socket + app unix sockets) → whole-desktop freeze, then false idle-reap storm — CONFIRMED

- Peer: `internal/router/shell_session.go:376-380` — `b.peerConn.Write(f.Payload)` runs inside the
  shell's single `ReadLoop` dispatch with no deadline (no `SetWriteDeadline` anywhere in
  `internal/wire` or the relay path). SSH TCP to B black-holes → ssh stops draining the `-L` socket
  → buffer fills → `Write` blocks the **entire** local dispatch loop: no keystrokes, no window ops,
  no ping processing, for up to ssh's keepalive exit (~45s).
- Apps: same loop does `b.app.writeRawFrame(...)` (shell_session.go:382) and
  `inst.WriteEvt(...)` (focus/resize/app_msg relays, e.g. shell_session.go:650-661,716-726) —
  synchronous writes to an app's unix socket via `StreamTransport`, also deadline-less, serialized
  under `inst.writeMu` (app_session.go:1008-1011). A stuck app BE (deadlock, SIGSTOP, NFS hang)
  fills its ~208 KiB socket buffer; the next relay to it blocks the dispatch loop; `inst.writeMu`
  then blocks every other goroutine writing to that app too.
- Compounding: liveness is stamped **in dispatch** (`shell_session.go:337`), not at the transport
  read — a blocked dispatch stops the stamp, so after 90s `readIdleLoop` reaps a *healthy* socket
  (transport.go:47), the FE reconnects, and the very next frame routed to the stuck endpoint
  re-freezes the new connection: a reconnect storm that never surfaces the actual culprit.
- Fix: per-peer-channel writer goroutine with bounded queue (closeChannel on overflow) or a write
  deadline on `peerConn`; write deadlines (or a bounded per-app outbound queue) for app-bound
  relays; stamp `lastReadAtNanos` at the transport read so a blocked dispatch is distinguishable
  from a dead socket.

### H4. Dead `?s=<sessid>` URL is a permanent, illegible reconnect loop behind wash-login — CONFIRMED

- `internal/login/server.go:576-603` (`handleRoot` serves shell.html for any `?s=` without
  validating), `:300` (`/ws/s/<dead>` 404s), `web/shell/src/main.tsx:343-349` (FE builds WS path
  from `?s=`), `web/shell/src/ws.ts:332-384` (`/auth/check` returns 204 — still authed — so the
  loop never goes `unauthenticated`).
- Routine trigger: attach a named session from the picker, leave the tab, session idle-reaps
  (default 30m, `internal/runner/router/router.go:246-249`). The tab shows "reconnecting…" forever;
  reload doesn't help because the URL keeps `?s=`. User must hand-edit the URL.
- Fix: validate `?s=` in `handleRoot` and redirect to `/sessions?err=session+ended`; and/or extend
  `/auth/check` with session liveness so ws.ts can surface "session ended" and navigate.

### H5. com.wash.remote leaks ssh tunnels (and host-B routers) on SIGTERM and router shutdown — CONFIRMED (both sub-reviews independently)

- `apps/remote/be/supervisor.go:130,236` (ssh under `exec.CommandContext` rooted at
  `context.Background()`, cancelled only by an explicit disconnect), `apps/remote/be/mounts.go:138`
  (mount ssh with no context at all). apps/remote registers **no** `sdk.OnTerminate` hook (repo-wide,
  only apps/vscode does; contrast `apps/vscode/be/server.go:117` Setpgid+Pdeathsig).
- Router SIGTERM (Settings restart, devreload) kills wash-remote without cancelling host ctxs → ssh
  orphans to PID 1; router crash/shutdown (conn-close → `sdk.Run` returns) likewise. Each orphan
  keeps the tunnel **and B's `--listen-raw` router** (ssh's remote command) alive — and `--listen-raw`
  has no idle timeout (only `--listen-unix` arms one, runner/router.go:246-249). One live B-side wash
  session accumulates per A-side restart. Exactly the vscode→code-server / display→Xwayland leak
  class `OnTerminate` was built to close.
- Fix: `sdk.OnTerminate` cancelling all host ctxs + killing the mount ssh; `Pdeathsig: SIGTERM` on
  the ssh cmd as backstop; consider an idle timeout for `--listen-raw`.

---

## MEDIUM

### M1. A `behind` terminal has no BE-side recovery trigger; output-only terminals can stay dark despite fd9069c — CONFIRMED mechanism

- `internal/router/router.go:1448-1476` (`resyncChannel` — only caller is `handleChannelCredit`,
  shell_session.go:549-569), `web/shell/src/credit.ts:37-49` (credit is purely consumption-driven,
  32 KiB threshold), `web/lib/src/terminal.tsx:427-430` (fd9069c stall watchdog fires **only after
  typed input**).
- The app_session.go:253-255 comment promises recovery by "credit recovery, reattach, or the
  watchdog" — there is **no BE watchdog**. Failing sequence: shared Bulk queue (64 frames,
  qos.go:38-47, per-connection **across all channels**) fills from concurrent bulk traffic (file
  download, display stream); terminal forward's `TrySubmit` fails **with credit remaining** →
  credit refunded, `behind=true` (shell_session.go:905-910, app_session.go:258-262). Because output
  is now suppressed, the FE absorbs nothing on that channel → never crosses the 32 KiB grant
  threshold → `handleChannelCredit` never fires → no resync. The credit-exhaustion wedge self-heals
  (the FE always owes ≥64 KiB > threshold once it drains); the scheduler-full wedge does not. A
  watch-only terminal (`tail -f`, a build) stays dark until the user types (8s nudge) or reloads.
- Fix: a per-shell ticker (or a hook on scheduler drain) that scans bindings with `behind=true` and
  calls `resyncChannel`; retry is already idempotent by design.

### M2. Crashed session app is never respawned; new shells get a chromeless desktop — CONFIRMED

- `internal/router/autoboot.go:29-42` — `r.session.started` set once, cleared only on registry-miss
  or spawn failure; `tearDown` never clears it. After a session-app crash every
  `EnsureSessionRunning` no-ops; no tombstone either (WindowID==0). Broken until router restart.
- Fix: clear the flag in the spawn-cleanup/tearDown path when the dying instance is the session app.
  Same pattern for M3.

### M3. Crashed background service is never respawned by shell connects — CONFIRMED

- `internal/router/autoboot.go:104-119` — `backgroundStarted[appID]` survives a crash, so
  `EnsureBackgroundAppsRunning` skips it forever. Sentinel-addressed app_msgs respawn singletons on
  demand (router.go:1244-1253), but push-only services (mDNS advertiser in com.wash.remote) have no
  inbound trigger — the host silently vanishes from LAN discovery until someone opens wash-connect.

### M4. Remote windows don't come back after an SSH blip unless the wash-connect window is open — CONFIRMED

- Only `apps/connect/fe/src/main.tsx:155-178` (`reconcileAttachments`, driven by `remote.state`
  pushes into a mounted Connect FE) ever re-issues `attachRemote` after a drop; the shell re-attaches
  peers only on **local** reconnect (`web/shell/src/main.tsx:1008-1038`). SSH drop with Connect
  closed: pump EOF → unbind → `detachClient` removes B's windows; supervisor auto-reconnects (status
  `up`, peer re-registered) → **nobody attaches**. Remote desktop silently gone until Connect is
  reopened. `peer.error` is console.warn-only (main.tsx:551-559).
- Fix: move the reconcile into an always-alive FE (shell or session app), surface `peer.error` via
  notify.

### M5. Deferred `mountWhenReady` resurrects a router-deleted window as an unclosable ghost — CONFIRMED trace

- `web/shell/src/wm.ts:296-315,283-288,367-376`. Snapshot filters the store synchronously but
  inserts asynchronously (bundle wait, up to 10s). A window whose bundle is still in flight when a
  reconnect snapshot / delete patch omits it isn't filtered (not in store yet); the deferred
  `upsertWindow` then lands it after the delete. Close button sends `window.close_clicked` for an
  unknown window → no destroy patch → ghost until the next reconnect. Fix: per-origin snapshot epoch;
  drop deferred upserts from a superseded epoch.

### M6. A self-closed relay socket wedges a remote client forever (frozen windows, no banner) — PLAUSIBLE

- `web/shell/src/relay-socket.ts:89-93` + `web/shell/src/main.tsx:752-754`: B's Conn factory is
  `() => sock` — it can only return the same, now-`closed`, `RelayChannelSocket`. If the socket
  closes itself (oversize/corrupt frame length in `drainBuffer`), B's Conn redials into a socket
  whose events never fire again → `'reconnecting'` forever, frames queueing to the 1 MiB cap, B's
  windows frozen with no banner and no unbind-driven cleanup. Fix: treat a non-detach relay-socket
  close as fatal for the origin (detachClient + re-attach) instead of retrying an unretryable
  transport.

### M7. Post-confirm window close sends SIGTERM with no escalation → permanently unopenable app — PLAUSIBLE

- `internal/router/shell_session.go:584-630`: on a confirmed close the window is destroyed
  immediately, then SIGTERM only (no grace→Kill ladder, unlike `restartBackgroundApp`). An app that
  confirms then hangs in shutdown stays in `r.apps` with its window gone; `launchOrRaise`
  (router.go:936-948) then "raises" the destroyed window instead of spawning, and `requestClose`'s
  in-progress guard (app_session.go:936) blocks a retry — unopenable until router restart. Fix: arm
  a grace timer that escalates to `Process.Kill()`.

---

## LOW

### L1. Reattach migration window can put a live Bulk frame on the wire before its Bind/replay — PLAUSIBLE

- `internal/router/router.go:1385-1431`: `b.shell = s` + snapshot happen under `shellMu`, but the
  Bind/replay are enqueued **after** unlock. A concurrent forward (app_session.go:228-266) can
  `TrySubmit` a live Bulk frame that the drainer writes before the Bind is even enqueued. Fresh
  page: `pendingRaw` flushes the live frame **before** the replay that chronologically precedes it
  (misordered bytes). `behind` stays false, so no resync will ever repair it. Tiny window, but it
  runs once per channel per reconnect under active output. Fix: send the resync/bind via
  `tryWriteCtrl` while still holding `shellMu` (the resyncChannel pattern), or mark the channel
  `behind` during migration and let the resync machinery deliver atomically.

### L2. Queued input silently discarded on `unauthenticated`; and multi-tab head-steal is silent — CONFIRMED

- `web/shell/src/ws.ts:343-345,591`: `clearPending()` without a `lost-input` event (the overflow
  path emits one). Keystrokes typed during the outage vanish unacknowledged.
- Design gap, worth an explicit UI: any new shell connection steals **all** terminal channels
  (router.go:1371-1376 head reassignment; non-head input dropped at shell_session.go:361-371). With
  two tabs, the loser's terminals go dark with only a router log line; after a blip, "last to
  reconnect" wins arbitrarily.

### L3. Teardown gated on `cmd.Wait` while a grandchild holds the stdout pipe — PLAUSIBLE

- `internal/router/spawn.go:74` (stdout is an OS pipe via MultiWriter) + router.go:915: `Wait`
  blocks until every inheritor of the pipe exits. A grandchild outliving a killed app delays
  tearDown indefinitely (window lingers, singleton slot pinned; `restartBackgroundApp`'s dedup can
  return the dying instance). Today's apps dodge it (vscode Pdeathsig; syslogs own pipes); it's a
  trap for the next child-spawning app. Fix: `cmd.WaitDelay` or don't gate teardown on Wait.

### Smaller notes (no separate writeups)

- 5523ef3 residual focus gaps: snapshot claim still adopted when `focused()==null` or when the
  claimed window is minimized (`web/shell/src/wm.ts:330-338`); the core cross-origin fix is correct
  and regression-tested. The term-badge half is complete.
- login first-spawn flock doesn't re-`List` under the lock (`internal/login/spawn.go:146-155` vs
  server.go:239-248) → two concurrently-reconnecting tabs can spawn two routers (silent session
  duplication, not a wedge).
- Bundles are re-shipped and re-imported on every live reconnect (fresh `bundleSent` per
  ShellSession, router.go:1306-1325); harmless (defineWashApp guards redefinition,
  web/lib/src/define-app.tsx:74) but wasted bandwidth + a scary "bundle FAILED" log on slow links.
- `connect()` from async `reconnectTick`: a throwing factory becomes an unhandled rejection that
  permanently kills the reconnect loop (unreachable with the stock factory; cheap to guard).
- `pendingRaw` buffers unboundedly for a channel that never gets a subscriber (`api.ts:199-204`).
- devreload SIGTERM has no escalation (devreload.go:199-208, dev-only); abandoned prepare_spawn
  tokens accumulate for router lifetime (app_session.go:876-891); `ReapWhenIdle` can cancel an
  attach that raced the final tick (unix_listener.go:504-534, self-heals); supervisor
  disconnect→connect click race can swallow the connect (supervisor.go:126-154, 381-389); login
  `Handoff` has no dial/send deadline (handoff.go:47,76 — only a truly deadlocked router triggers
  it); fd9069c's stall watchdog false-positives on no-echo input (password prompts) — affordance
  only, the nudge is harmless.

---

## What looks solid (verified end-to-end)

- **Non-blocking forward / behind / ring (Fix B core)**: unconditional byte-exact tee into the ring
  (app_session.go:228-231); `TryReserve`+`TrySubmit` with refund keeps the per-app read goroutine
  from ever blocking on a wedged FE; resync runs entirely under `shellMu` with non-blocking enqueues
  so snapshot and resumed live stream cannot interleave (router.go:1448-1476); credit-exhaustion
  wedges self-heal via the FE's ≥64 KiB debt crossing the 32 KiB grant threshold. Pinned by
  wedge_repro_test.go (all four wedges) and credit tests.
- **Head ownership (Fix A)**: head sampled before `shellMu` (documented lock order,
  router.go:1088-1100), head adopts on input, non-head dropped, peer channels exempt — pinned by
  TestWedge_HeadInputNotDropped / BackgroundShellInputDropped.
- **Drainer teardown (Fix C)**: wsWriteTimeout on every FE write (transport.go:23,87-99); failed
  write → scheduler.Close → all blocked producers unblock with ErrSchedulerClosed; HandleShell joins
  the drainer before banking stats; every producer path submits via the scheduler, so nothing can
  outlive it. Pinned by TestWedge_SlowClientHeadOfLine + TestWSWriteTimeout.
- **Idle reap (Fix D) + 718abe5**: watchdog arms only after the first ping (legacy FEs never
  reaped); any inbound frame stamps liveness; Worker-driven FE heartbeat immune to main-thread
  starvation, ticks reused across reconnects, `sendPing` guarded by state; 90s is a sane backstop
  for 15s pings. The one caveat is H3's dispatch-located liveness stamp.
- **FE offline queue & zombie detection**: queue-while-down with FIFO flush, all-or-nothing overflow
  drop surfaced as `lost-input`; wake probes (visible/online/pageshow) + forceRedial that nulls the
  dead socket's handlers; `/auth/check` cleanly separates expired-cookie (terminal, legible `/login`
  messaging) from server-down (retry forever). ws.test.ts pins these.
- **Banner**: pure render of `connState` — cannot stick shown or hidden.
- **BE-owned view state (persist helper)**: per-origin `replaceSavedStates` on snapshot; `wash:state`
  dispatched only on (re)mount so reconnect never re-applies stale state over live FE state;
  debounced saveState flushed on unmount and queued through outages. Apply-or-lose holds.
- **Login front statelessness across router restart**: /proc is the sole source of truth
  (sessions.go:79-104); dead routers vanish from List; handoff failure → 502 → FE retry → clean
  respawn/attach; handoff protocol bounded (16 KiB replay cap, 5s header deadline, SO_PEERCRED,
  exactly-one-fd). dd72d78 is complete (WaitGroup joins both WS and /app/ handoff paths;
  ctx-cancelled Close skips the 5s WS handshake); 42d6698 likewise joins raw-relay sessions.
- **SSH drop today is clean, not a wedge** (M2e absence notwithstanding): ssh exit → pump EOF →
  closeChannel closes peerConn + unbinds → shell drops B's RouterClient + windows; B's router dies
  with the ssh session; supervisor reconnects with health-gated backoff and classified diagnostics
  (7225744 works as advertised); relayed routers refuse nesting; `handlePeerAttach` idempotent per
  (shell, origin); pump's lock-free `b.shell` read is safe by the pinned-peer invariant
  (reattach skips live peer bindings). The gaps are M4 (re-attach) and H3 (blocking write).
- **Child lifecycle (where wired)**: sdk.OnTerminate dual-trigger (signal + conn-close), run-once,
  panic-guarded; vscode's full Setpgid/Pdeathsig/killTree ladder; tearDown removes every map entry
  (multi-window byWin scan) with no skippable early return; the spawn pid-race fix is complete
  (`pendingMu` held across Spawn+register, token path shares the mutex); restartBackgroundApp has
  proper expectedExit + SIGKILL escalation + failure-clears-flag.
- **Scheduler**: strict-priority with at-most-one out-of-priority frame per race, FIFO per class;
  file-channel unbind rides Bulk so it can't overtake its payload; bundle/replay/asset flows are
  size-completed so class reordering can't truncate them.

## Test gaps worth closing (from the traces above)

1. Same-router live reconnect with an open terminal: assert no duplicated scrollback (H1).
2. wake/reconnectNow while a dial is in flight (H2).
3. A blocked peer/app write must not stall unrelated dispatch (H3).
4. Dead `?s=` → picker redirect (H4); router-restart-under-login List→refused→respawn.
5. behind=true via scheduler-full (not credit) → recovery without keyboard input (M1).
6. Session-app / background-service crash → respawn on next shell connect (M2/M3).
7. SSH drop → supervisor reconnect → windows re-attach with Connect closed (M4).
