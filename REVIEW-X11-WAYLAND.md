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
| **5** popover classifier over-matches untitled dialogs | ❌ Unresolved (investigated ×2, no robust signal) | `d06c51f` (D7) tightening REVERTED in `4f978bc`; a second pass (`ec89a8d`, G5) also rejected GTK-app_id / decoration-mode / min-max / commit-timing / content-probing — the toolkits expose no menu-vs-dialog role on a Wayland xdg_toplevel. A **titled** dialog already stays a window. Known limitation — TODO.md. |
| **6** video resync corruption | ✅ Fixed | `590434e` (D5 pt1): router skips the ring replay for video kinds + FE clears its canvas. `f4a372f` (D5 pt2): new `window.force_frame` router→app event → compositor `reset_delta()` re-emits a full frame. |
| **7** popup positioning + no unconstrain | ✅ Fixed | `9931764` (D6): subtract the popup's geometry origin + `wlr_xdg_popup_unconstrain_from_box`. |
| **8** unmap→remap staleness | ✅ Fixed | `f125c91` (D8): `sink_open` calls `WindowSink::reset_delta()` → full first frame. |
| **9** stuck modifiers / hover | ✅ Fixed | `805fb98` (D8): FE releases held keys on blur/hide; compositor clears pointer focus on unfocus. |
| **10** wheel deltaMode ignored | ✅ Fixed | `f1df0fe` (D8): FE normalizes deltaMode + sends notch count; BE emits clean value120. |
| **11** browser autorepeat forwarded | ✅ Fixed | `aeb7c33` (D8): FE drops `ev.repeat`; the Wayland client is the single repeat authority. |
| **12** title changes after map | ✅ Fixed | `bacc312` (D8): `set_title` listeners (xdg + xwayland) → new `report_title`. |
| **13** keycode gaps + keymap/selection notes | ✅ Fixed | `b97ec33` (D8): numpad, `IntlBackslash`, `ContextMenu`. Selection thread leak `261c147` (G3): poll()-bounded read. Keymap-layout hint `f2ad961` (G2): FE `getLayoutMap` → new `display.set_keymap` app_msg → compositor recompiles xkb keymap (conservative fr/de/us; full per-key remap a future note). |
| **min/max size** | ✅ Fixed | `6e2c272` (G4): xdg toplevel min/max → `create_window` → `SessionWindow.{Min,Max}{W,H}` → FE clamps `applyResize` to [min,max]. |
| Protocol-coverage gaps (DnD, primary selection, viewporter, presentation-time, xdg-activation, Xwayland `-auth`, minimize/icons, touch) | 🚧 In progress (Phase H) | Net-new protocol support, user-approved; H6 (Xwayland `-auth`, security) first. |

"What looks solid" below was verified and left unchanged.

---

## Findings (ranked)

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
