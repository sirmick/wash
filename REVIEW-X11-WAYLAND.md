# wash-display X11/Wayland correctness review

Scope: `wash-display/src` (compositor.cpp, capture.cpp, encode/webp, main.cpp, wsframe),
`cpp-sdk/wash/wire_conn.*`, FE `web/shell/src/wash-app-display.ts`, Go side
(`internal/pty` env mapping, `internal/router` channel/QoS/spawn, `internal/login/spawn.go`),
judged against how GTK3/4, Qt5/6, Chromium/Electron, SDL, Java/AWT and xterm-class X11
apps actually drive Wayland/Xwayland, and against wlroots 0.17 idioms (vendored tree read
for ground truth). Verified prior work: HiDPI integer-scale is genuinely closed end-to-end
(integer 1|2 clamp, physical/logical split is consistent BE↔FE, no fractional-scale manager
so clients can't request fractions); the Qt serial-less popover fallback (8e9d819) exists and
works for the intended case — but its classifier over-matches (finding 5).

## Executive summary

The core architecture is sound and unusually well-commented: per-surface capture →
WebP → per-window channel, single-threaded wlroots loop with self-pipe marshalling,
wlroots' own xdg-popup grab machinery reused rather than reimplemented. Input, clipboard,
popups, CSD strategy and HiDPI hold up under scrutiny. The serious problems are all
"virtual desktop vs. real desktop" mismatches: every surface is stacked at scene origin
(0,0), so wlroots' occlusion culling silently starves frame callbacks for any window covered
by a newer one (freezing it for the user, who can still see it); X11 clients can never
resize themselves because `request_configure` is unhandled; the compositor cannot exit when
its control socket dies; and the multi-user login path never provisions a per-user
XDG_RUNTIME_DIR, so wash-display is broken or unsafe behind wash-login. Below those, a
cluster of medium findings (popover misclassification of GTK dialogs, PTY-shaped video QoS
with no keyframe recovery, popup unconstrain/geometry-offset, remap staleness, stuck
modifiers, wheel-delta units) each map to a concrete real-app failure.

---

## Resolution status (2026-07-02, merged to local main)

Findings 1–4, 6–12 fixed; 5 attempted and reverted; 13 partly fixed. Branch
fix-display-correctness (+ fix-video-forceframe for #6 part 2). Commit hashes
are the local-main merge commits.

| # | Status | Where |
| --- | --- | --- |
| **1** occlusion starves frame callbacks (CRITICAL) | ✅ Fixed | `1934a1a` (D1): `setenv WLR_SCENE_DISABLE_VISIBILITY=1`. **Deviation** from the doc's scene-position spread — that pushes windows outside the single normal-sized virtual output and loses frame-done just as badly; the env var is a sanctioned option here and near-free (capture reads surfaces directly). |
| **2** X11 `request_configure` unhandled | ✅ Fixed | `014f460` (D2): listener → `wlr_xwayland_surface_configure`; size propagates via `sink_frame`. |
| **3** compositor never exits when the wire dies | ✅ Fixed | `3d5caa5` (D3): `WireConn::on_disconnect` → self-pipe → `wl_display_terminate`. Also fixes the orphan-compositor leak. |
| **4** no per-user XDG_RUNTIME_DIR behind login | ✅ Fixed | `dd0b1af` (D4): `childEnv` sets `<runRoot>/<uid>/xdg`; router creates it 0700 in the setuid context. |
| **5** popover classifier over-matches untitled dialogs | ❌ Reverted / unresolved | `d06c51f` (D7) tightening REVERTED in `4f978bc` — every candidate signal (no-decoration / no-min-max / recent-pointer) misclassifies real Qt menus (Qt sets decoration+min/max, and serial-less programmatic menus have no pointer event). A **titled** dialog already stays a window. Known limitation — TODO.md. |
| **6** video resync corruption | ✅ Fixed | `590434e` (D5 pt1): router skips the ring replay for video kinds + FE clears its canvas. `f4a372f` (D5 pt2): new `window.force_frame` router→app event → compositor `reset_delta()` re-emits a full frame. |
| **7** popup positioning + no unconstrain | ✅ Fixed | `9931764` (D6): subtract the popup's geometry origin + `wlr_xdg_popup_unconstrain_from_box`. |
| **8** unmap→remap staleness | ✅ Fixed | `f125c91` (D8): `sink_open` calls `WindowSink::reset_delta()` → full first frame. |
| **9** stuck modifiers / hover | ✅ Fixed | `805fb98` (D8): FE releases held keys on blur/hide; compositor clears pointer focus on unfocus. |
| **10** wheel deltaMode ignored | ✅ Fixed | `f1df0fe` (D8): FE normalizes deltaMode + sends notch count; BE emits clean value120. |
| **11** browser autorepeat forwarded | ✅ Fixed | `aeb7c33` (D8): FE drops `ev.repeat`; the Wayland client is the single repeat authority. |
| **12** title changes after map | ✅ Fixed | `bacc312` (D8): `set_title` listeners (xdg + xwayland) → new `report_title`. |
| **13** keycode gaps + keymap/selection notes | ⚠️ Partial | `b97ec33` (D8): numpad, `IntlBackslash`, `ContextMenu` added. **Keymap-layout hint** (non-US host) and **`handle_set_selection` blocking-thread** still open — TODO.md. |
| Protocol-coverage gaps (DnD, primary selection, viewporter, presentation-time, xdg-activation, min/max, Xwayland `-auth`, minimize/icons, touch) | ⏸ Deferred | Out of scope — TODO.md. |

"What looks solid" below was verified and left unchanged.

---

## Findings (ranked)

### 1. Occluded windows starve of frame callbacks → visible windows freeze
**Severity: Critical · Confidence: CONFIRMED (code trace through vendored wlroots)**

All surfaces are scened at the tree origin with no per-window position:
- `wash-display/src/compositor.cpp:1088` (`wlr_scene_xdg_surface_create(&server->scene->tree, …)`, no `wlr_scene_node_set_position`)
- `wash-display/src/compositor.cpp:1320` (X11: `wlr_scene_surface_create(&x->server->scene->tree, …)`)

New scene nodes append at the top of the stacking order
(`third_party/wlroots/types/scene/wlr_scene.c:73`, insert at `children.prev`). wlroots scene
visibility culling (`calculate_visibility`, on by default) subtracts the opaque regions of
nodes above (`wlr_scene.c:455-459`), and frame callbacks are gated on visibility:
`wlr_scene_buffer_send_frame_done` fires only if `node.visible` is non-empty
(`wlr_scene.c:818-823`), and `scene_node_send_frame_done` additionally requires
`primary_output == scene_output`, which is NULL when the visible∩output intersection is
empty (`wlr_scene.c:344-390, 1922-1946`).

Failing sequence: open window A (e.g. Chromium or foot — both set full opaque regions;
Xwayland marks alpha-less X windows fully opaque), then open window B the same size or
larger. A's visible region is now empty → no frame callbacks → A throttles: Chromium's
rAF-driven compositor stalls completely, foot/GTK stop repainting, typing into A (focus is
router-authoritative, so the user CAN focus A) produces no visual update. In wash both
windows are independently visible in the browser, so this is not the "hidden window"
semantics the culling was designed for. GTK CSD shadow rings partially mask the bug (the
translucent margin keeps a sliver visible), which is why small-window testing wouldn't show
it — a maximized/equal-size window triggers it reliably.

Fix direction: give each mapped toplevel a distinct far-apart scene position (e.g.
`index * kMaxScreenW` in x), or run with `WLR_SCENE_DISABLE_VISIBILITY=1`, or stop relying
on `wlr_scene_output_send_frame_done` and send frame-done per captured surface tree
in `output_frame`.

### 2. X11 `request_configure` unhandled — X apps can never resize/position themselves
**Severity: High · Confidence: CONFIRMED**

`server_new_xwayland_surface` (`compositor.cpp:1368-1388`) wires
associate/dissociate/destroy only. `xwm_handle_configure_request`
(`third_party/wlroots/xwayland/xwm.c:994-1020`) only emits
`events.request_configure`; if no listener calls `wlr_xwayland_surface_configure`, the
managed (substructure-redirected) window never gets its ConfigureNotify and keeps its old
geometry. There is no `request_configure` listener anywhere in `compositor.cpp`.

Real-app failures: xterm's Ctrl+RightClick font-size change (self-XResizeWindow) does
nothing; Java/AWT `pack()`/`setSize()` after window creation leaves the frame at its
creation size (classic "tiny Java window"); Tk `wm geometry`; any X dialog that autosizes
after mapping. Pre-map configure requests are also dropped, so `xsurface_map`'s
`wlr_xwayland_surface_configure(…, xsurf->width, xsurf->height)` (`compositor.cpp:1272`)
re-confirms stale creation-time geometry.

Fix direction: listen for `request_configure`, apply via
`wlr_xwayland_surface_configure(ev->x, ev->y, ev->width, ev->height)`, and when mapped,
propagate the new size to the wash window (`report_geometry`/resize).

### 3. Compositor never exits when the wire connection dies → orphan Xwayland/guest tree
**Severity: High · Confidence: CONFIRMED**

`run_compositor` blocks in `wl_display_run` (`compositor.cpp:2230`); nothing ever calls
`wl_display_terminate`. When the router vanishes without SIGTERM (crash, SIGKILL, e2e
teardown races), `WireConn::reader_loop` exits and sets `alive_=false`
(`cpp-sdk/wash/wire_conn.cpp:250-259`) — but the wlroots loop keeps running forever.
The connection-close reap in `main.cpp:321-326` (`reap_child_group()` after `run()`)
is dead code in the compositor build because `run()` never returns; the comment there
believes otherwise. Every write just fails with EPIPE (SIGPIPE ignored). Result: an
orphan wash-display + Xwayland + WASH_DISPLAY_EXEC guest per unclean router death,
plus stale `wayland-N` sockets accumulating in XDG_RUNTIME_DIR. This is the same leak
class the SIGTERM/group-kill work fixed, minus its trigger.

Fix direction: on `reader_loop` exit (or an `alive` transition), write the existing
self-pipe with a "terminate" command and call `wl_display_terminate` in `on_cmd_pipe`.

### 4. Multi-user (wash-login) path: no per-user XDG_RUNTIME_DIR → display DOA or cross-uid
**Severity: High (prod/multi-user only) · Confidence: CONFIRMED (env plumbing trace)**

`childEnv` (`internal/login/spawn.go:332-354`) re-points only HOME/USER/LOGNAME for the
setuid per-user router; XDG_RUNTIME_DIR passes through from wash-login's own environment
(or is absent under a system service). wash-display inherits it via `Spawn`
(`internal/router/spawn.go:58`). Consequences:
- unset → `wl_display_add_socket_auto` fails (libwayland requires XDG_RUNTIME_DIR) →
  compositor exits at startup: no display layer at all behind wash-login;
- set to wash-system's `/run/user/<wash-system-uid>` → all users' compositors try to create
  sockets in a directory they don't own (fails), or — if permissions ever allow — sockets
  for different users land in one shared dir, violating the same-uid 0700 requirement and
  letting sessions dial each other's compositors. `WASH_XDG_RUNTIME_DIR` is then also
  published into every terminal (`compositor.cpp:2211-2213`, `internal/pty/pty.go:430-442`),
  propagating the wrong dir to clients.

Fix direction: wash-login (or the router at startup) must ensure a per-uid 0700 runtime dir
(e.g. `/run/wash/<uid>/xdg` created in the setuid context, like the sessions dir) and set
XDG_RUNTIME_DIR for the router/its children.

### 5. Popover classifier over-matches: untitled parented GTK/Qt dialogs become pointer-grabbing overlays
**Severity: Medium-High · Confidence: PLAUSIBLE→likely (classifier logic confirmed; toolkit behavior well-known)**

`toplevel_is_popover` (`compositor.cpp:825-841`) treats any parented xdg_toplevel that is
untitled (or title == app_id) and smaller than ¾ of the screen as a Qt menu fallback:
overlay at the last pointer position + `push_popup_grab` on the parent
(`compositor.cpp:872-914`). But GNOME HIG message dialogs deliberately have empty titles:
gedit's "Save changes?" close-confirmation, GTK4 `AlertDialog`/`MessageDialog`, many Qt
progress/tool windows. These map as parented, untitled, small toplevels → rendered as a
chromeless overlay anchored wherever the pointer happened to be (clamped to ≥0; a
keyboard-triggered dialog anchors at a stale position), unmovable, and the compositor-side
grab redirects ALL parent-window pointer input to the dialog until it unmaps. Clicks
"outside" reach the dialog at negative coordinates — a dialog, unlike a menu, does not
self-dismiss, so the parent window is mouse-dead for the dialog's lifetime (works for
modal dialogs by accident, wrong for non-modal ones).

Also: nested serial-less Qt submenus fail the `pt->sink.popover` check
(`compositor.cpp:880`) and fall back to standalone chromeless windows titled "Window" —
a Qt programmatic menu → submenu chain visually explodes.

Fix direction: require menu-like evidence beyond "untitled + parented" — e.g. no
xdg-decoration object, no min/max size, maps within a short window after a pointer event,
and/or small absolute size; let nested popovers chain off the popover's parent win with
accumulated offsets.

### 6. Video channels ride PTY-shaped QoS: FE-behind ⇒ torn replay + permanently stale frames
**Severity: Medium-High · Confidence: CONFIRMED mechanism; trigger needs a slow FE**

`video`/`video-popup` are Bulk + credit-gated (`cpp-sdk/wash/wire_conn.cpp:70-73`,
`internal/router/router.go:667-669`). When the FE stops granting credit, the router
suppresses live Bulk frames and marks the channel behind
(`internal/router/app_session.go:242-265`); recovery is `resyncChannel`
(`internal/router/router.go:1448-1476`), which replays the ring buffer through
`realignReplay` — a **terminal-escape** realignment — as one raw blob. For a video channel
this replay is a concatenation of 45-byte-header WebP frames, possibly truncated
mid-frame: the FE (`wash-app-display.ts:474-539`) parses the first 45 bytes as a header and
hands the rest to `createImageBitmap` — at best one frame decodes, the rest are discarded;
`wash-app-display.ts` registers no resync handler and has no way to request a full frame.
Meanwhile the compositor has already adopted the per-surface seqs for every suppressed
frame (`capture.cpp:380`), so the lost damage is never re-sent: the canvas keeps stale
regions indefinitely (the delta protocol assumes lossless delivery). VP9-over-WebRTC will
make this worse (inter-frames).

Real scenario: mpv or Chromium video in one window saturates the credit window / the WS while
the browser main thread is busy → that window (or an unrelated one sharing the wedge) shows
mixed stale/new regions forever.

Fix direction: on `channel.resync` for video kinds, drop the ring instead of replaying, and
give the compositor a "force full frame" nudge (a control frame on the channel, or reset
`tree_sig`/`states_` when the router reports the channel went behind). Long-term: treat video
as droppable-with-keyframe-recovery, not lossless.

### 7. xdg_popup positioner: no unconstrain, and popup's own geometry offset not subtracted
**Severity: Medium · Confidence: CONFIRMED (both by absence + scene idiom comparison)**

a) No call to `wlr_xdg_popup_unconstrain_from_box` anywhere → `constraint_adjustment`
(flip/slide) is never applied. GTK/Qt rely on the compositor for edge avoidance; a context
menu invoked near the bottom/right of the virtual screen keeps its natural placement, and
the FE overlay (`position:fixed`, `wash-app-display.ts:631-696`) will happily extend past
the browser viewport — unreachable menu items. Real case: gedit/nautilus context menu at
the bottom edge; Qt combo dropdown near the screen edge.

b) `popup_root_and_offset` (`compositor.cpp:932-958`) accumulates
`popup->current.geometry.x/y` but the capture sends the **full popup surface** including
its CSD shadow margin, without subtracting the popup's own window-geometry origin. wlroots'
own scene helper positions the surface at `-geo.x, -geo.y` under the geometry-positioned
tree (`third_party/wlroots/types/scene/xdg_shell.c:52-68`). Net effect: GTK4/Qt menus render
shifted down-right by their shadow margin (GTK4 margins are ~24-32px), submenus misalign
with their parent item, and the grab-path outside-click hitbox (`off_x/off_y`,
`compositor.cpp:802-810, 1522-1532`) is shifted by the same amount. Input *within* the
overlay is self-consistent (full-surface coords), which is why menus still work — they're
just in the wrong place.

Fix direction: after map/commit, apply
`off -= popup->base->current.geometry.{x,y}` (and same for the popover path), and call
`wlr_xdg_popup_unconstrain_from_box` with the virtual-output box at map.

### 8. Unmap→remap: capture/sink state survives, first frames are partial → mostly-blank window
**Severity: Medium · Confidence: CONFIRMED logic trace**

`toplevel_unmap`/`xsurface_unmap` close the wash window but keep the `WindowSink`:
`SurfaceCapture::states_`, `tree_sig`, `sent_w/h`, encoder state all persist
(`compositor.cpp:613-621, 1281-1291`). On remap a NEW wash window + empty canvas is created,
but the first captures compute damage against the pre-unmap `states_` (`capture.cpp:157-201`)
— only the regions that changed while hidden are sent. A GTK app that hides and re-presents
a window (`gtk_widget_set_visible` + `gtk_window_present`), or an X11 app remapping a dialog
(GIMP-style tool dialogs, apps that withdraw/remap on workspace hints), reappears mostly
blank until something forces full damage. Fix: reset `tree_sig`, `states_` and `sent_w/h`
(or pass `force_full` once) on map.

### 9. Stuck modifiers and hover state on focus/pointer loss
**Severity: Medium · Confidence: CONFIRMED (no code path exists to clear)**

The FE forwards only raw downs/ups (`wash-app-display.ts:276-291`); there is no blur/
visibilitychange handler releasing held keys, and the compositor's `window.unfocus` only
clears seat focus (`compositor.cpp:1873-1883`) — the vkbd's xkb state keeps depressed
modifiers, which are re-sent on the next `keyboard_notify_enter`. Alt-Tabbing the host
browser while a guest is focused leaves Alt (or Ctrl/Shift) latched: next focus, GTK opens
menus on plain keys, terminals see Ctrl-chords. Similarly no pointer-leave is ever sent
(`g_ptr_surface` stays entered), so hover highlights/tooltips stick when the mouse leaves
the wash window. Fix: FE sends synthetic key-ups for tracked downs on blur; compositor
clears pointer focus on unfocus and adds a "pointer left" event.

### 10. Wheel delta units: deltaMode ignored; pixel deltas passed as value120
**Severity: Medium · Confidence: CONFIRMED for Firefox-host; PLAUSIBLE degradation elsewhere**

`onWheel` forwards `ev.deltaY` raw (`wash-app-display.ts:264-274`) and the compositor treats
it as a 120-per-notch value120 (`compositor.cpp:1601-1613`). `ev.deltaMode` is never
checked: Firefox delivers **line** deltas (±3 per notch) → value = 3/120·15 ≈ 0.4 logical
px and discrete=3 → scrolling in every guest is ~40× too slow when wash is used from
Firefox. In Chrome (pixel deltas ~100-120/notch) continuous scroll is roughly right, but
discrete-consuming clients are off: Xwayland converts value120→buttons 4/5 by accumulating
to ±120, so non-multiple-of-120 deltas make xterm/xclock-class scrolling skip/lag; Qt 6
accumulates value120 for list-view stepping with the same drift. Fix: normalize deltaMode
(lines→×~40 px or straight to notches), and send an explicit notch count so the BE can emit
clean `value120 = notches*120`.

### 11. Browser key autorepeat forwarded as fresh presses (double repeat / protocol violation)
**Severity: Low-Medium · Confidence: PLAUSIBLE**

`onKeyDown` forwards `ev.repeat` events as new downs (`wash-app-display.ts:276-285`, comment
says this is deliberate), and `wlr_keyboard_notify_key` does not dedupe already-pressed keys
(`third_party/wlroots/types/wlr_keyboard.c:99-119`). Wayland clients are told repeat is
their job via `repeat_info` (defaults rate=25/delay=600 set by `wlr_keyboard_init`), so a
held key produces both toolkit-timer repeats and forwarded browser repeats — with
timer-reset behavior differing per toolkit the result ranges from correct-by-luck to
double-rate repeat; sending press for an already-pressed key is also a spec violation some
toolkits log or mishandle. Xwayland/X clients are fine (looks like X autorepeat). Fix: drop
`ev.repeat === true` downs and let `repeat_info` clients do it, or suppress client repeat by
setting repeat_info rate=0 and keep forwarding browser repeats (pick ONE repeat authority).

### 12. Title changes after map never propagate
**Severity: Low · Confidence: CONFIRMED (no listeners)**

No `xdg_toplevel->events.set_title` or `xwayland_surface->events.set_title` listener; the
wash window title is snapshotted at map (`compositor.cpp:596-597, 1266-1267`). xterm dynamic
titles, Chromium tab-title changes, editors' "file — modified" markers all stay stale.

### 13. Misc smaller correctness notes
- `code_to_keycode` (`compositor.cpp:1460-1494`) has no numpad, `IntlBackslash`,
  `ContextMenu`, media keys → those keys are dead in guests (numpad matters for
  spreadsheets/CAD).
- Keymap is hard-coded to the system default (`compositor.cpp:2099-2110`); a browser user
  on AZERTY typing into wash gets... whatever the *server's* layout is. Because the FE
  sends physical codes, layout mismatch host↔guest produces wrong characters for any
  non-US host layout. Needs a layout hint from the FE (or `KeyboardEvent.key`-based
  fallback mapping).
- `handle_set_selection`'s reader thread blocks forever if the selection owner exits
  without writing (`compositor.cpp:1725-1733`) — leaked thread+fd per occurrence.
- `destroy_window` decrements the panel's window count even when create failed paths call
  it with win previously 0 — guarded, OK — but `note_window_delta(-1)` on a failed
  `confirm_close` veto race can double-count (cosmetic, settings panel only).
- `toplevel_map` never passes the xdg parent to `create_window` (parent param exists in
  the wire, `wire_conn.cpp:262-292`) — titled dialogs don't stack/group above their parent.
- X11 override-redirect popup geometry is sent once at map (`compositor.cpp:1237-1239`);
  a menu that moves itself after map (rare, but combo autoscroll does) leaves the overlay
  at the old offset.

---

## Protocol-coverage gaps (what real toolkits want, ranked by blast radius)

1. **wl_data_device drag-and-drop**: `seat.events.request_start_drag` is never handled →
   `wlr_seat_start_pointer_drag` never called → **in-app** DnD is dead (GTK text drag in
   gedit, file drag inside a Qt file view, Chromium tab-drag gesture semantics). This
   breaks single-app behavior, not just cross-app transfer.
2. **Primary selection** (`zwp_primary_selection_device_manager_v1` / seat
   `request_set_primary_selection`): absent → middle-click paste dead in terminals
   (foot/alacritty native-Wayland) and X↔Wayland primary is unbridged.
3. **xdg_positioner unconstrain** (see finding 7) — GTK/Qt menus at screen edges.
4. **wp_viewporter**: not advertised. GTK4 and Chromium cope; SDL2's
   `SDL_HINT_VIDEO_WAYLAND_MODE_SCALING` and mpv's `--wayland-...` fractional paths degrade
   gracefully. Low urgency but cheap (`wlr_viewporter_create`), and `surface_logical_w`
   (`capture.cpp:104-116`) already handles `current.width` correctly if it appears.
5. **presentation-time**: absent; Chromium/Firefox fall back to frame callbacks (fine), but
   video A/V sync in mpv is degraded.
6. **xdg-activation**: absent → "focus me" requests (URL handoff to an open browser) are
   silently dropped.
7. **min/max size**: `toplevel->current.min/max_width/height` never consulted; the FE's
   interactive resize clamps only to 100×60 (`wash-app-display.ts:26-27`) and the router can
   configure a size below the client minimum — clients clamp themselves and the geometry
   report re-syncs, so it self-heals, but the drag feels rubber-bandy for Qt apps with
   hard minimums.
8. **Xwayland X-server access control**: wlroots starts Xwayland without `-auth`; any local
   uid can connect to `:N`. Same-machine multi-user deployments (the wash-login estate)
   leak X windows/input across users. Pair with finding 4's runtime-dir work.
9. **request_minimize / set_app_id / icons**: ignored — minimize buttons in CSD do nothing;
   wash windows can't show per-app icons.
10. **Touch**: not advertised (fine — FE only synthesizes pointer events); note pointer-only
   `wl_seat` capabilities are declared correctly.

## What looks solid

- **xdg-shell configure discipline**: initial-commit handling is correct — wlroots 0.17
  itself schedules the first configure (`wlr_xdg_toplevel.c:135-138`) and the compositor's
  `set_size(0,0)` lets clients pick their size; no buffer-before-configure hazards.
- **Decoration strategy**: forcing CLIENT_SIDE + chromeless wash windows for Wayland, wash
  frames for X11 — exactly one titlebar in all cases, with the request_move/resize relays
  (M8/M8b) making CSD drags drive the wash WM. Clean design, correct serial story.
- **Popup grabs**: reusing wlroots' internal xdg_popup grab machinery (pointer+keyboard
  grabs, same-client enter filtering, click-outside dismiss) instead of reimplementing —
  keyboard menu navigation works because the keyboard grab keeps delivery inside the
  menu's client. The compositor-side `g_popup_grabs` redirection composes correctly with it.
- **Input subsurface descent** (`wlr_surface_surface_at` in `focus_child`,
  `compositor.cpp:1566-1577`): the Chromium/Electron subsurface click bug was correctly
  diagnosed and fixed; coordinates stay in the right spaces (canvas→geometry-crop→surface).
- **Damage/dirty-rect design** (`capture.cpp`): per-surface commit-seq tracking with
  layout-change/vanish handling, multi-commit full-bounds fallback, and adopting seqs only
  after successful read-back — carefully reasoned and correct in isolation (finding 6 is
  about the transport assuming this stream is lossless, not about the math).
- **Pixel pipeline**: ARGB render target + ABGR (GL_RGBA) read-back rationale, R/B swap, and
  premultiplied→straight alpha conversion for WebP are all correct, including the a=0/255
  fast paths; FE `clearRect`-then-`drawImage` matches the straight-alpha contract.
- **Threading model**: single wlroots thread + self-pipe marshalling for window commands,
  input, and clipboard offers; blocking wire calls kept off the reader thread with the
  deadlock rules documented at every seam. `write_mu_` serializes the fd correctly.
- **HiDPI**: integer output scale is applied consistently (output mode/scale, capture
  scale, `physical_to_logical` ceil matches FE `Math.round` in all reachable cases,
  logical input coords) — the "closed" gap is genuinely closed for integer scales.
- **Process hygiene**: crash-handler backtraces, SIGPIPE ignore, process-group claim +
  group-kill on SIGTERM, and the SIGCHLD/xkbcomp interaction note are all right — modulo
  finding 3's unreachable connection-close path.
- **Clipboard bridge**: eager-vs-lazy impedance match (guest→wash read-through-pipe on a
  worker; wash→guest lazy source), loop guard via `wash_source`, X11 covered by xwm — sound.
