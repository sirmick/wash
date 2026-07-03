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

*All findings addressed — see the resolution-status table above. (Full detail in git history.)*

---

## MEDIUM

*All findings addressed — see the resolution-status table above. (Full detail in git history.)*

---

## LOW

### L2. Queued input silently discarded on `unauthenticated`; and multi-tab head-steal is silent — CONFIRMED

- `web/shell/src/ws.ts:343-345,591`: `clearPending()` without a `lost-input` event (the overflow
  path emits one). Keystrokes typed during the outage vanish unacknowledged.
- Design gap, worth an explicit UI: any new shell connection steals **all** terminal channels
  (router.go:1371-1376 head reassignment; non-head input dropped at shell_session.go:361-371). With
  two tabs, the loser's terminals go dark with only a router log line; after a blip, "last to
  reconnect" wins arbitrarily.

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
