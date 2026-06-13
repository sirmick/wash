# Reconnectability Audit — 2026-06-12

Scope: what survives a browser/shell disconnect (refresh, network drop, second
tab) and what leaks. Covers the core router/shell machinery, every windowed
app's FE state, shell (terminal) sessions, and the background-service tier.

## Fix status (branch wash-reconnect, 2026-06-12)

| Finding | Status |
|---|---|
| A1 app_msg dropped while detached | Invariant documented (ARCHITECTURE.md "Reconnect & durability"); worst consumer fixed via B1. Push-buffering deliberately NOT added — pull-on-mount is the contract. |
| A2 FE writes lost during WS-down | FIXED — bounded 1MiB FIFO queue in ws.ts, flushed on reopen, dropped whole on overflow/auth-death. Unit-tested. |
| A3 app_state dies with router | Documented as a deliberate tier (ARCHITECTURE.md). No code change. |
| A4 second tab gets no raw bytes | Documented as v1 single-shell (ARCHITECTURE.md). No code change. |
| A5 FE map residue | Not fixed — bounded, no correctness impact. |
| B1 stale tab after pty exit while detached | FIXED — term FE sends `list_sessions` after restore; BE replies with live set; FE drops dead tabs / adopts unknown live ones. e2e: term-reconcile.spec.ts. |
| B2 replay starts mid-sequence | FIXED (partial) — wrap-gated `realignReplay` trims torn UTF-8/CSI heads before replay. Lost DECSET modes + torn OSC remain (needs a VT-state tracker; not attempted). |
| B3 replay not reflowed on resize | Not fixed — xterm-side reflow of historical bytes isn't tractable; cosmetic until next redraw. |
| B4 scrollback cap | By design; documented. |
| C washamp/music/radio playback state | FIXED — all three persist track/position/volume (+shuffle/repeat for washamp, favorites already there for radio); restore to paused-at-position, resume on click. Washamp skin choice remains unrecoverable (Webamp discards the skin source URL; no skin-change event). |
| edit 250ms debounce window | Mitigated by A2 (saves queue while offline). Debounce itself unchanged. |

The reconnect contract, as built: app BE processes survive shell disconnects;
WM geometry/z/focus/min-max state and per-instance `app_state` blobs live in
the router's `windowSession` (internal/router/wmstate.go) and are re-delivered
as a snapshot + `wash:state` on remount; raw channels get a 256KB scrollback
ring replayed on reattach with `(channel, window, kind)` re-announced
(internal/router/router.go:1062). Anything important outside that loop leaks.

## A. Cross-cutting holes (core machinery)

### A1. BE→FE app_msg is silently dropped while no shell is attached — HIGH
`relayAppMsgToShell` (internal/router/app_session.go:496) loops over
`shellList()`; with zero shells the message vanishes. Only raw channels get
ring-buffer capture (app_session.go:223-236). **This makes "FE must pull on
mount" the system invariant** — every app whose FE depends on a BE push that
can fire while detached must re-sync on remount. Apps that do (fm
`request_initial`, priv `hello`, net `loadCurrent`, sidebar resubscribes) are
fine; apps that don't (see per-app and term findings) desync.

### A2. FE→router writes during the WS-down window are silently lost — HIGH
`Conn.sendCtrl`/`sendRaw` (web/shell/src/ws.ts:208, :221) call `ws.send()`
with no readyState check and no outbound queue. Between socket death and
reconnect, keystrokes, app_msgs — **including `save_state` persists** — are
dropped without error. Combined with debounced saves (edit's 250ms,
apps/edit/fe) the state mutated just before a drop is unrecoverable.

### A3. `app_state` survives shell reconnect but not router restart — MED (by design, undocumented)
`windowSession.appState` is router-process memory only (wmstate.go:21). A
router restart (frequent in the dev loop) loses every window position and
app-state blob. Disk-backed state (desktop.json via settings, priv audit log,
radio favorites via BE) survives. Worth one line in ARCHITECTURE.md so apps
know which tier they're choosing.

### A4. Two simultaneous shells: raw channels bind to exactly one — MED
Ctrl/app_msg broadcasts go to all shells (`shellList()`), but a raw channel
has a single `b.shell` (router.go channelBinding); reattach claims only
*detached* bindings (router.go:1078). A second tab gets windows and app_state
but **no terminal bytes** for channels owned by the first tab, and saved-state
updates reach it only on refresh (acknowledged in web/shell/src/api.ts:128).
Multi-shell is effectively half-supported; either document single-shell or
gate the second attach.

### A5. FE map residue across in-page reconnect — LOW
`pendingRaw`/`videoChannelForWindow` (web/shell/src/api.ts) are cleared per
channel on unbind, and `replaceSavedStates` resets blobs on snapshot, but a
reconnect that re-uses the page (no full reload) can leave queued bytes for
channel ids that will never rebind. Bounded leak, no correctness impact seen.

## B. Shell (terminal) session holes

What works (e2e-verified: e2e/tests/term-reattach.spec.ts): pty + child shell
survive detach; 256KB replay restores recent scrollback; tabs + font_id +
font_size persist via `save_state`; window geometry survives
(session-reattach.spec.ts).

### B1. pty exits while detached → stale tab restored on reconnect — HIGH
BE onClose sends `tab_closed` via `SendAppMsg` (apps/term/be/app.go:310) —
dropped per A1. The persisted tab list is **FE-written** (`persist`,
apps/term/fe/src/main.tsx:140), so the BE can't amend it while detached. On
remount `restoreFrom` (main.tsx:152) re-adds every saved tab with no liveness
check and there is no mount-time list-sessions handshake with the BE. Result:
a dead tab renders as a frozen terminal; input goes to a closed channel; no
exit status is ever shown. (Single-tab case is clean: BE `ConfirmClose`
removes the window router-side; the snapshot simply omits it.)
Fix shape: FE sends `list_sessions` on mount, BE replies from `st.sessions`;
reconcile tabs.

### B2. Replay is a byte tail — mid-sequence starts and lost terminal modes — MED
The ring buffer (internal/router/ringbuf.go) overwrites byte-wise; `Snapshot`
can begin mid-ANSI-escape or mid-UTF-8 rune after wraparound — no realignment
before `term.write(bytes)`. Worse, DECSET state (alt-screen, bracketed paste,
application cursor keys, mouse reporting) set *before* the 256KB window is
not reconstructed: reattach into a long-running vim/htop can render garbage
or lose modes. A tiny VT-state tracker (or BE-side `tput`-style reset + mode
re-emit on reattach) would cover the common cases.

### B3. Geometry change across reconnect: replay not reflowed, resize race — MED
Replay bytes were rendered for the old cols/rows; the FE re-measures and
sends resize after mount (web/lib/src/terminal.tsx:307, skipped when
unchanged), so reconnecting on a different display shows wrongly-wrapped
replay and the SIGWINCH lands after replay is on screen. Cosmetic until the
next full redraw, but reliably reproducible.

### B4. Scrollback beyond 256KB is gone after reload — LOW (by design)
xterm.js's own buffer dies with the page; recovery window =
`ChannelScrollbackBytes` only. Note it in docs so it's a chosen tradeoff.

### B5. Keystrokes during the down window dropped — covered by A2.

## C. Per-app FE state (windowed apps)

Solid — follow these as the reference pattern:
- **fm**: persists path/expansion/sort/hidden/info/split, restores on
  `wash:state`, falls back to `request_initial` (apps/fm/fe/src/main.tsx:1598).
- **vscode-workbench**: persists folder; code-server iframe session is
  same-origin and server-side, survives independently.
- **session** (chrome): sidebar mode + section states persisted; desktop
  config is disk-backed (desktop.json + fswatcher).
- **net**: no persistence, but `loadCurrent()` on mount re-syncs from netd;
  losing draft edits on reload is the right call.
- **settings**: stateless re-reader, nothing to leak.

Leaks, by pain:

| App | Verdict | What leaks on reconnect |
|---|---|---|
| **edit** | minor, severe edge | Untitled-buffer content/cursor *is* persisted, but behind a 250ms debounce riding the WS (A2) — content typed just before a drop is lost; no BE recovery ask on mount (apps/edit/fe/src/main.tsx:640-682). Embedded terminal sessions intentionally not restored. |
| **washamp** | broken | No `wash:state` handler at all: playlist, now-playing, position, skin all reset (apps/washamp/fe/src/main.tsx). |
| **music** | broken | Only the folder persists; track index + seek position are FE-only (apps/music/fe/src/main.tsx:39-149). |
| **radio** | partial | Favorites/custom/last-station persist and re-select, but play state + volume reset (apps/radio/fe/src/main.tsx:39-199). |
| journal / syslogs / top / disks / packages / services | acceptable | View/filter/selection ephemeral; live-data apps, defensible — but unit selection (journal) and file selection (syslogs) would be cheap to persist. |

Note on the media trio: actual audio output lives in the browser, so playback
*sound* necessarily stops on reload — but position/queue/volume should be
persisted so "press play resumes where you were."

## D. Background services / sidebar

What works: `sdk.StateService` replies to every subscribe with a full
snapshot (internal/sdk/stateservice.go:71-83), subscribe is idempotent
(map-as-set), and the session FE re-subscribes to all five services on every
mount (apps/session/fe/src/main.tsx:661-665) — so sidebar state heals on
reconnect even though pushes to a detached shell are dropped (the session BE
is a pure relay, apps/session/be/app.go:118, and caches nothing).

- **bulk** — solid: pending conflicts live *inside* the published state
  (apps/bulk/be/app.go:42-47); workers block on a channel until resolved, so
  a prompt raised while detached re-presents from the snapshot.
- **notify** — solid for reconnect: in-memory history (cap 100,
  apps/notify/be/app.go:67) captures notifications raised while detached;
  toasts are transient by design. History dies with the notify process —
  acceptable, but it's the same undocumented tier as A3.
- **priv** — solid: pending approvals are BE state; FE sends `hello` on
  mount (apps/priv/be/app.go:125-127); audit log is disk-backed; no secret
  parked FE-side beyond the in-flight password field.
- **stale subscribers** — accepted by design (stateservice.go:23-27): dead
  instance ids accumulate; router drops sends to gone instances. Fine at
  current scale.

## E. Test-coverage gaps

Covered: term reattach scrollback, session window geometry, app-state
round-trip, reconnect→login legibility. Not covered: pty-exit-while-detached
(B1), replay after ring wraparound (B2), resize-across-reconnect (B3),
keystroke/save loss during the down window (A2), media app state (C),
two-tab behavior (A4).

## Priority order

1. **A1+B1**: term mount-time `list_sessions` reconcile (closes the worst
   user-visible hole) and write down the "FE must pull on mount" invariant.
2. **A2**: readyState guard + small outbound queue (or at least drop-with-
   banner) in ws.ts; flush pending `save_state` before unload.
3. **C**: washamp/music/radio playback-state persistence.
4. **B2/B3**: replay hygiene (UTF-8/CSI realignment, mode re-emit, reflow).
5. **A3/A4**: document the durability tiers and the single-shell assumption.
