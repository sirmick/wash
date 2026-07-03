# Implementation prompt: fix the findings from the 2026-07-01 correctness reviews

> **STATUS (2026-07-02): COMPLETE — all four phases + D5 part 2 merged to local `main`.**
> Per-finding resolution is recorded at the top of each review doc
> (REVIEW-DATAPATH.md, REVIEW-RECONNECT.md, REVIEW-X11-WAYLAND.md); the
> remaining/out-of-scope backlog is in repo-root `TODO.md` ("2026-07-01
> correctness-review leftovers"). Deviations: **D1** used
> `WLR_SCENE_DISABLE_VISIBILITY=1` instead of scene-position spread (the spread
> breaks frame-done against a single normal-sized output); **D7** was reverted —
> the popover-classifier tightening can't be done without regressing real Qt
> menus. Nothing pushed to a remote. 31 fix commits from baseline `718abe5`.

You are implementing fixes for confirmed correctness bugs in the wash codebase. Three review
documents at the repo root are your source of truth — **read the relevant section of the review
before starting each fix**, they contain exact file:line traces and failure scenarios:

- `REVIEW-DATAPATH.md` — wire/router/PTY data plane (findings F1–F11)
- `REVIEW-RECONNECT.md` — reconnect lifecycle (findings H1–H5, M1–M7, L1–L3)
- `REVIEW-X11-WAYLAND.md` — wash-display compositor (findings 1–13 + protocol gaps)

Work through the phases below **in order**. Do not reorder: Phase C is only safe after Phase A.
Do not attempt fixes not listed here without asking the user first.

**FIRST ACTION, before reading code or editing anything: create the phase's worktree.** Never
commit work directly on `main` or on whatever branch happens to be checked out:

```
git -C /home/mick/wash worktree add branches/fix-reliability-a -b fix-reliability-a
cd /home/mick/wash/branches/fix-reliability-a   # do ALL work for the phase in here
```

## Ground rules (do not skip)

1. **One worktree branch per phase** under `branches/` (as above; Phase B →
   `branches/fix-reliability-b`, Phase C → `branches/fix-qos-class`, Phase D →
   `branches/fix-display-correctness`), merged back to local `main` when the phase is fully
   green, then `git worktree remove` it before starting the next phase.
   Remember: `e2e/` is not a pnpm workspace member — in a fresh worktree run
   `pnpm install --ignore-workspace` inside `e2e/`.
2. **Green gate**: commit only on `make build` + unit tests green; before merging a phase run
   the full suite including `make test-race` and `make e2e-test` (NOT raw playwright — the
   login fixture needs the multicall binary layout). Push only if the user asks.
3. **Each numbered fix = one commit**, message style `fix(<component>): <what>` matching the
   existing log.
4. **Do not refactor** beyond what a fix requires. The reviews each have a "what looks solid"
   section listing verified invariants (head ownership lock order, resync-under-shellMu
   atomicity, scheduler close semantics, copy-on-write for snapshot-outliving slices, the
   single-threaded wlroots loop + self-pipe marshalling). Do not restructure those mechanisms;
   work within them.
5. **Every fix gets a test** (unit or e2e as specified). If a specified test is impractical,
   say so explicitly in the commit message rather than silently skipping.
6. If a fix turns out to be larger or riskier than described here, **stop and ask the user**
   instead of improvising.
7. CBOR pitfall: never introduce `json.RawMessage` / `[]byte` in structured BE→FE fields (the
   router base64-encodes byte strings).

---

## Phase A — reconnect/data-plane core (branch `fix-reliability-a`)

### A1. Resync on live reattach (REVIEW-RECONNECT H1 == REVIEW-DATAPATH F3) — do this first
- In `reattachChannelsToShell` (`internal/router/router.go:1371-1433`), send a
  `wire.NewShellChannelResync(...)` for each generic channel **before** the ring replay
  (mirroring what `resyncChannel` at `router.go:1448` sends). The FE's `deliverResync` is a
  no-op when the channel has no subscriber (`web/shell/src/api.ts:235-236`), so the
  page-refresh path is unaffected while a live-page reconnect gets the xterm reset + mode
  re-seed (`web/lib/src/terminal.tsx:547-555`).
- Keep the existing `ShellChannelBind` + replay; only add the resync in front.
- While here, close L1 (REVIEW-RECONNECT): the bind/replay are currently enqueued after
  `shellMu` is released, so a concurrent live frame can hit the wire first. Either enqueue
  bind+resync+replay via the non-blocking `tryWriteCtrl`/`tryWriteRaw*` helpers while still
  holding `shellMu` (the `resyncChannel` pattern), or set `behind = true` during migration and
  let `resyncChannel` deliver everything atomically.
- **Test**: new e2e — open a terminal, `echo MARKER`, force a WS drop **without page reload**
  (close the socket from the page via `__washDiag` / evaluate, or kill the TCP conn), wait for
  reconnect, assert exactly one MARKER in the terminal buffer. Existing refresh spec
  (`e2e/tests/term-reattach.spec.ts`) must stay green.

### A2. FE concurrent-dial race (REVIEW-RECONNECT H2)
- `web/shell/src/ws.ts`: in `connect()` (lines ~269-307) capture `const sock = this.ws` and
  make every handler (`onopen`, `onmessage`, `onclose`, `onerror`) bail immediately when
  `sock !== this.ws`. Make `reconnectNow()` (~515-521) a no-op while a dial is already in
  flight (add an explicit `dialing` flag set in `connect()`, cleared when the socket settles),
  or tear the in-flight socket down (null handlers, close) before redialing. Add a reentrancy
  guard around the `authGone()` await in `reconnectTick` (~341-350).
- **Test**: extend `web/shell/src/ws.test.ts` — trigger `wake('online')`/`reconnectNow()`
  while a dial is in flight (mock socket held in CONNECTING); assert only one socket ends up
  attached, queued sends flush exactly once, and the stale socket's `onclose` does not
  schedule another reconnect.

### A3. BE-side behind-recovery watchdog (REVIEW-RECONNECT M1 == REVIEW-DATAPATH F2)
- Recovery from `behind=true` is currently only triggered by an inbound credit grant
  (`shell_session.go:549-569` → `resyncChannel`), which a suppressed channel stops
  generating. Add a per-shell ticker (a few seconds; reuse the `readIdleLoop` pattern in
  `internal/router/transport.go`) that scans this shell's bindings for `behind == true` and
  calls `resyncChannel` for each. `resyncChannel` is already idempotent and tolerates
  TrySubmit failure — call it as-is, do not modify its internals.
- Also fix the stale comment at `router.go:1447` ("or the watchdog") to describe the now-real
  watchdog, and the promise at `app_session.go:253-255`.
- **Test**: unit test in `internal/router` modeled on `wedge_repro_test.go` — wedge via a
  **full Bulk scheduler queue with credit remaining** (this is the non-self-healing variant),
  do NOT send any input or credit grant, assert the channel resyncs within the watchdog
  interval.

### A4. Resync must not overtake queued same-channel Bulk frames (REVIEW-DATAPATH F4)
- `resyncChannel` (`internal/router/router.go:1448-1476`) currently emits the reset at
  ClassControl and the snapshot at ClassInteractive, which strict priority drains ahead of
  stale same-channel frames still sitting in the Bulk queue → FE sees
  `reset, snapshot, STALE bulk duplicates`. Fix by emitting both reset and snapshot at
  **ClassBulk** so they stay FIFO behind the stale tail (the deferral logic already tolerates
  TrySubmit failure). This is required before Phase C makes terminals ride Bulk.
- **Test**: unit test — enqueue Bulk frames for a channel, trigger resync, drain the
  scheduler, assert the FE-visible order is `stale frames…, reset, snapshot, live…` (no
  bytes delivered twice after the reset).

Phase A merge gate: full unit + race + e2e green, then merge to local main.

---

## Phase B — lifecycle leaks & failure legibility (branch `fix-reliability-b`)

### B1. Deadline-less blocking writes in the shell dispatch loop (REVIEW-RECONNECT H3 == REVIEW-DATAPATH F5/F6)
Three sub-steps, in this order, each its own commit:
1. **Liveness stamp**: move/duplicate the `lastReadAtNanos` stamp so it is written at the
   transport read itself, not only inside `dispatch` (`shell_session.go:337`) — a busy-but-alive
   dispatch must never be reaped by `readIdleLoop`.
2. **Peer writes**: give the peer relay write (`shell_session.go:376-380`,
   `b.peerConn.Write`) a write deadline (e.g. 30s, matching `wsWriteTimeout` in
   `transport.go`); on deadline, close that peer channel (existing closeChannel path) instead
   of blocking dispatch.
3. **App-socket writes**: give app-bound writes (`app_session.go:1008-1018` `writeRawFrame`,
   and the `WriteEvt` relays) a write deadline via `SetWriteDeadline` on the unix conn; on
   deadline treat the app as wedged (log + tear down that instance the same way a dead conn
   is handled). Alternative per the review — a bounded per-app egress queue + writer
   goroutine — is acceptable but bigger; prefer the deadline unless it proves insufficient.
- Do NOT move asset streaming off the dispatch loop in this pass (`handleAssetRead`) — with
  sub-step 1 the reaper false-trip is gone; leave a `// TODO(review F5)` comment instead.
- **Test**: unit test — a fake app conn that stops reading; assert dispatch survives (a
  subsequent unrelated frame is processed) and the healthy shell conn is NOT idle-reaped.

### B2. com.wash.remote leaks ssh tunnels + host-B routers (REVIEW-RECONNECT H5)
- `apps/remote/be/`: register an `sdk.OnTerminate` hook (copy the pattern from
  `apps/vscode/be/server.go:117`) that cancels all host supervisor contexts
  (`supervisor.go:130,236`) and kills the mount ssh (`mounts.go:138` — give it a context).
  Add `Pdeathsig: SIGTERM` to the ssh `exec.Cmd`s as backstop.
- **Test**: unit-level — fake command runner asserting OnTerminate cancels every live host
  ctx; if the supervisor is already seam-tested, extend that.

### B3. Crashed session/background apps never respawn (REVIEW-RECONNECT M2+M3)
- `internal/router/autoboot.go`: clear `r.session.started` (lines ~29-42) and
  `backgroundStarted[appID]` (~104-119) in the instance tear-down path when the dying
  instance is that app, so the next `EnsureSessionRunning`/`EnsureBackgroundAppsRunning`
  respawns it.
- **Test**: unit test — start session app, simulate crash/tearDown, call Ensure again, assert
  respawn (and the same for a background app).

### B4. Dead `?s=<sessid>` is a permanent reconnect loop (REVIEW-RECONNECT H4)
- `internal/login/server.go` `handleRoot` (~576-603): before serving shell.html for a `?s=`
  request, validate the session exists (same /proc-derived List used by `handleSessions`); if
  dead, redirect to `/sessions?err=session+ended`.
- **Test**: extend the login unit tests — request `/?s=bogus`, assert 302 to `/sessions`.

### B5. Remote windows never re-attach after an SSH blip unless Connect is open (REVIEW-RECONNECT M4)
- Move the reconcile currently in `apps/connect/fe/src/main.tsx:155-178`
  (`reconcileAttachments`) so it also runs without the Connect window: the shell FE
  (`web/shell/src/main.tsx` — it already handles peer re-attach on local reconnect at
  ~1008-1038) should subscribe to the same `remote.state` pushes and re-issue `attachRemote`
  for hosts marked attached whose peer is down. Surface `peer.error` via the notify service
  instead of console.warn (`main.tsx:551-559`).
- This is the largest Phase B item; if the remote.state plumbing to the shell is not
  straightforward, stop and ask the user before inventing new message types.
- **Test**: if the two-VM harness (`make e2e-remote-vm`) is available, add: attach B, kill
  the ssh tunnel, wait for supervisor reconnect, assert B's windows return with the Connect
  window closed. If the harness can't run in your environment, note it and cover the FE logic
  with a unit test instead.

Phase B merge gate: full unit + race + e2e green, then merge.

---

## Phase C — PTY output onto the real Bulk/credit path (branch `fix-qos-class`)

**Prerequisite: Phase A merged (A3 + A4 specifically).** This flip activates the credit /
behind / resync machinery for every terminal; without A3/A4 it converts latent bugs into
visible terminal freezes and corruption.

### C1. Generic raw channels write Bulk (REVIEW-DATAPATH F1)
- `internal/sdk/channel.go`: `writeClass` zero value is ClassInteractive and only
  `ChannelKindFile` sets Bulk (line ~197). Per `docs/QOS.md` ("raw channels default to Bulk
  at OPEN time"), make the PTY/terminal channel write ClassBulk. Prefer an explicit opt-in at
  the pty open site (`internal/pty/pty.go:257-296`) over silently changing the zero value —
  audit every other `OpenChannel` caller (grep `OpenChannel(` across the repo) and decide
  per-caller; list the decision for each caller in the commit message.
- Verify the router side debits credit for these frames now
  (`internal/router/app_session.go:252` gates on `class == wire.ClassBulk && b.credit != nil`)
  — check whether generic channels get a credit ledger at `registerChannel`
  (`router.go:666-672`); if only video kinds do, extend it to pty channels, matching what the
  wedge tests assume.
- **Tests**: (1) integration test driving a real `pty.Session` asserting the wire class of
  its output frames is Bulk; (2) the full existing wedge/e2e suite
  (`term-wedge-recovery.spec.ts`) — it now exercises the real path; (3) manual soak note:
  run `yes` and `cat` a large file in a terminal while dragging windows — no input lag in
  other apps, terminal recovers when the flood stops.

### C2. FE credit hygiene (REVIEW-DATAPATH F8 + F7)
- Only grant credit for Bulk-class raw frames: `web/shell/src/ws.ts` has `classOf(flags)` but
  discards the class before `onRaw`; plumb the class through so `main.tsx:596` calls
  `credit.absorbed` only for Bulk frames.
- Cap `pendingRaw` per channel (`web/shell/src/api.ts:105-110,192-205`) — drop-oldest with a
  console warning; and do not grant credit for bytes parked in `pendingRaw` (grant on real
  subscriber consumption).
- **Test**: unit tests for both behaviors in the web test suite.

Phase C merge gate: full unit + race + e2e green **plus** the manual soak above; report soak
results to the user before merging.

---

## Phase D — wash-display fixes (branch `fix-display-correctness`)

C++ in `wash-display/src` (single-threaded wlroots loop; all wire I/O off the reader thread —
respect the documented threading rules at each seam). Build with the existing CMake setup;
there is a smoke harness under `tmp/` from earlier compositor work if still present.

### D1. Occlusion starves frame callbacks (REVIEW-X11-WAYLAND 1 — CRITICAL)
- Every surface is scened at (0,0) (`compositor.cpp:1088`, `:1320`), so wlroots visibility
  culling gives covered windows an empty visible region and
  `wlr_scene_buffer_send_frame_done` never fires for them. Fix: give each mapped
  toplevel/X-surface a distinct far-apart scene position via `wlr_scene_node_set_position`
  (e.g. `slot_index * 100000` in x, reusing freed slots) so no two windows ever overlap in
  scene space. Capture reads the surface directly (comment at `compositor.cpp:1054`), so
  position is otherwise irrelevant — but verify pointer/input coordinate translation does not
  use scene-absolute coords anywhere (grep for `wlr_scene_node_coords` /
  `wlr_scene_buffer_point_accepts_input` / `node_at`) before assuming this is safe; if input
  does use scene coords, translate at the seam.
- **Test**: manual — two same-size opaque windows (e.g. two foot/xterm instances maximized);
  type into the older one; it must keep repainting. Add this to the display e2e if the
  harness supports two guests.

### D2. X11 `request_configure` unhandled (REVIEW-X11-WAYLAND 2)
- Add a `request_configure` listener in `server_new_xwayland_surface`
  (`compositor.cpp:1368-1388`): call
  `wlr_xwayland_surface_configure(xsurf, ev->x, ev->y, ev->width, ev->height)`, and when the
  surface is mapped, propagate the new size to the wash window (the existing
  `report_geometry`/resize path).
- **Test**: manual — `xterm`, Ctrl+RightClick → change font size → window must resize.

### D3. Compositor never exits when the wire dies (REVIEW-X11-WAYLAND 3)
- On `WireConn::reader_loop` exit (`cpp-sdk/wash/wire_conn.cpp:250-259` sets
  `alive_=false`), notify the compositor: write a "terminate" command byte to the existing
  self-pipe and have `on_cmd_pipe` call `wl_display_terminate`. This makes the
  connection-close reap in `main.cpp:321-326` reachable (it currently sits after a `run()`
  that never returns — fix or remove its stale comment).
- **Test**: manual/scripted — start wash-display under the router, `kill -9` the router,
  assert wash-display and its Xwayland exit within seconds (no orphan processes,
  no stale `wayland-N` socket).

### D4. Per-user XDG_RUNTIME_DIR behind wash-login (REVIEW-X11-WAYLAND 4)
- `internal/login/spawn.go` `childEnv` (~332-354): provision a per-uid 0700 runtime dir
  (e.g. `/run/wash/<uid>/xdg`, created in the setuid context like the sessions dir) and set
  `XDG_RUNTIME_DIR` in the child env. Follow the existing pattern for the sessions dir.
- **Test**: unit test on `childEnv` asserting XDG_RUNTIME_DIR is set per-user and the dir is
  0700 owned by the target uid (mode assertions may behave differently under root/umask in
  CI — follow the existing file-mode test patterns in the repo).

### D5. Video resync corruption (REVIEW-X11-WAYLAND 6; pairs with A3)
- The ring replay is meaningless for video framing. In the router, for video channel kinds,
  make resync skip the ring replay (send the reset only), and add a compositor-side "force
  full frame" reaction: on learning the channel went behind/resynced, clear the capture's
  delta state (`tree_sig`, `states_`, `sent_w/h` in `capture.cpp`) so the next capture sends
  a full frame. The FE (`wash-app-display.ts`) should handle `channel.resync` by clearing its
  canvas-pending state (it currently registers no resync handler).
- If plumbing the behind-notification to the compositor requires a new wire message, stop and
  ask the user first; the reset-only + FE-clears-canvas half is safe to do alone.
- **Test**: unit test that video-kind resync sends no ring bytes; manual check that a display
  window recovers visually after a forced wedge.

### D6. Popup positioning + unconstrain (REVIEW-X11-WAYLAND 7)
- Subtract the popup's own window-geometry origin: in `popup_root_and_offset`
  (`compositor.cpp:932-958`) apply `off -= popup->base->current.geometry.{x,y}` (compare
  wlroots' own `types/scene/xdg_shell.c:52-68`), and apply the same correction to the
  grab-path outside-click hitbox (`compositor.cpp:802-810, 1522-1532`).
- Call `wlr_xdg_popup_unconstrain_from_box` with the virtual-output box when the popup maps.
- **Test**: manual — GTK4 app (or gedit) context menu: it must align under the pointer, and
  a menu opened at the bottom edge must flip/slide to stay on-screen.

### D7. Popover classifier over-match (REVIEW-X11-WAYLAND 5)
- Tighten `toplevel_is_popover` (`compositor.cpp:825-841`): require menu-like evidence beyond
  untitled+parented+small — at minimum: no xdg-decoration object requested, no min/max size
  set, and mapped shortly after a pointer event (a timestamp you already have from the last
  forwarded pointer input). Untitled GTK message dialogs must fall through to normal window
  handling.
- Nested serial-less submenus (`compositor.cpp:880` sink.popover check): allow a popover to
  parent another popover, chaining offsets. If this proves intricate, do only the classifier
  tightening and leave a TODO for nesting.
- **Test**: manual — a Qt app menu still overlays correctly; a gedit-style close-confirm
  dialog appears as a normal movable window.

### D8. Small display fixes (one commit each, all low risk)
- Remap staleness (finding 8): on map, reset `tree_sig`, `states_`, `sent_w/h` (or set a
  `force_full` flag) so a remapped window repaints fully.
- Stuck modifiers/hover (finding 9): FE sends synthetic key-ups for all tracked held keys on
  window blur/visibilitychange (`wash-app-display.ts:276-291`); compositor clears pointer
  focus on `window.unfocus` (`compositor.cpp:1873-1883`).
- Wheel deltaMode (finding 10): in `onWheel` (`wash-app-display.ts:264-274`) normalize
  `ev.deltaMode` (lines → ×40px, pages → viewport height) and send an explicit notch count so
  the compositor emits clean `value120 = notches*120` (`compositor.cpp:1601-1613`).
- Key autorepeat (finding 11): drop `ev.repeat === true` keydowns in the FE — Wayland clients
  repeat themselves via `repeat_info`; pick that as the single repeat authority.
- Title updates (finding 12): add `set_title` listeners (xdg + xwayland) that update the wash
  window title after map.
- Keycode gaps (finding 13): extend `code_to_keycode` (`compositor.cpp:1460-1494`) with
  numpad, `IntlBackslash`, `ContextMenu`.

Phase D merge gate: builds green, Go unit suite green, display e2e green
(watch for the stale-binary gotcha: e2e uses the `out/` compositor binary — rebuild it, check
its mtime, and kill orphan compositors before judging failures), plus the manual checks above
reported to the user.

---

## Explicitly OUT of scope (do not do without being asked)

- REVIEW-DATAPATH F9/F10/F11 (readloop goroutine leak, relay max-frame, fuzz/Reserve notes).
- REVIEW-RECONNECT M5/M6/M7, L2/L3 and the "smaller notes" list.
- REVIEW-X11-WAYLAND protocol-coverage gaps (DnD, primary selection, viewporter,
  presentation-time, xdg-activation, min/max size, Xwayland -auth, minimize/icons).
- Moving asset streaming off the dispatch loop (deferred half of B1).
- Any VP9/WebRTC work.

These are documented in the reviews; leave them there. When all four phases are merged and
green, update `TODO.md` with the out-of-scope leftovers (one line each, referencing the
review doc + finding ID), and report a phase-by-phase summary of what was fixed, what tests
were added, and any deviations.
