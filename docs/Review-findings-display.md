# wash-display correctness review — 2026-09-23

Scope: `wash-display/src/compositor.cpp`, `capture.cpp`, `cpp-sdk/wash/wire_conn.*`,
`web/shell/src/wash-app-display.ts`, and the router's video-channel paths, read
end to end and then compared line-by-line against the reference compositors
checked out under the gitignored `reference/` (wlroots 0.17.4, sway 1.9,
labwc 0.7.4). Every item below was confirmed against reference source before
being fixed; the ones that turned out to be correct are listed at the end so
nobody re-audits them.

All fixes landed on branch `wash-display-review` together with the tests that
pin them (see §Tests). Numbering matches the `REVIEW-DISPLAY-2026-09 #n`
markers in the code.

## Fixed

| # | Symptom | Root cause | Fix |
|---|---|---|---|
| 1 | X11 app → wash copy silently delivered nothing (Wayland leg mostly worked) | `handle_set_selection` closed the pipe's write end right after `wlr_data_source_send`. wlroots' send OWNS that fd: a Wayland client source closes it itself, and xwm's X11 source stores it and writes later from its own event source — so the X11 transfer went into whatever reused that fd number | Don't close it. Log `clipboard guest->wash stored N bytes` after a complete read so tests can assert the transfer, not just the offer |
| 2 | After the lazy X server's first restart, every override-redirect window grabbed the parent (tooltips froze input) | `xatom()` kept a static xcb connection from the FIRST Xwayland; after the 10 s lazy exit + restart it returned NULL replies, `XCB_ATOM_NONE` was cached forever and every type check failed → the untyped fallback | Connect + intern on the Xwayland `ready` event (fires per restart), never cache a failed intern |
| 3 | Steam toasts, Wine helpers, Electron/Java popups killed the parent window's input while mapped | untyped override-redirect ⇒ "grab" by default; wlroots' own classifier treats untyped as a normal window | Grab only for the four explicit menu types. Real untyped X menus (Xt/Motif/xterm) hold an X grab inside Xwayland, which routes outside-clicks itself (what sway relies on) |
| 4 | A window mapped while no browser was attached never streamed, even after one attached | `channel.open` failed with "no shell attached", `video_chan` stayed 0, nothing retried | `sink_ensure_channel` retries on the first router→app command for that window (focus/resize/force_frame all imply a shell is looking) |
| 5 | Stale rectangles after a burst of frames | FE decoded each frame with an unordered `createImageBitmap` promise: a big full frame finishing after a later small dirty frame overwrote it with older pixels | `SerialQueue` (display-frames.ts) chains decode+draw in wire order, per window and per popup |
| 6 | X11 fullscreen (mpv/SDL/games) stalled or letterboxed; X11 maximize/minimize ignored; X tooltips/scrolling menus/autocomplete drawn at their map-time position; focused X window never raised in the X stack | no `request_fullscreen` / `request_maximize` / `request_minimize` / `set_geometry` handlers, no restack (xwm flips `_NET_WM_STATE` BEFORE emitting, so the app believed it was fullscreen at the old size) | Handlers configure to the output (or the size xwm saved) and mirror the state to the wash window; `set_geometry` re-sends the overlay offset and updates the grab; restack ABOVE on focus |
| 7 | Submenus / menus of a second X app drawn on the wrong wash window | override-redirect parent fallback was "most recently mapped X toplevel" | Resolve via the transient-for chain → same `pid` → keyboard-focused X surface → most recent |
| 8 | Fixed-size X dialogs rubber-banded on resize | ICCCM size hints not forwarded (zeros to `window.create`) | Forward `size_hints` min/max like the xdg path |
| 9 | Menus near a window edge ran off the browser viewport, or flipped by the CSD margin too early | `wlr_xdg_popup_unconstrain_from_box` got `{0,0,W,H}`: wrong space (needs toplevel-surface coords, i.e. + xdg geometry offset, per the wlroots header and sway) and the compositor had no idea where the wash window sits in the browser | FE sends the browser viewport in canvas coords with each input batch (`vp`); unconstrain on the popup's INITIAL commit (like labwc) to `{vp.x+geo.x, vp.y+geo.y, vp.w, vp.h}`, falling back to the output box offset by geo |
| 10 | A menu flipped above/left of its window painted once then froze | popups are scened at the root at their own offset; `wlr_scene_output_send_frame_done` only serves buffers overlapping the output box (the `WLR_SCENE_DISABLE_VISIBILITY` env does not change that) | overlay commits schedule an output frame; `output_frame` sends frame_done to every overlay surface itself |
| 11 | After a menu closed, the window under the pointer stopped reacting to hover until the cursor left it | a private "already entered" cache skipped `notify_enter`, but wlroots' popup grab had REFUSED that enter (foreign client → focus cleared) | Removed the cache; `notify_enter` on every event (wlroots de-dups, sway does the same) |
| 12 | Spurious pointer leave / possible focus loss on `window.unfocus` | keyboard + pointer focus cleared unconditionally | Clear only if this window's surface holds it (sway/labwc guard); FE also sends a real `leave` on `pointerleave` so hover highlights still clear |
| 13 | Submenus of a Qt programmatic (menu-fallback) menu were invisible | `popup_root_and_offset` required the parent toplevel to own a wash win; a popover has none → "no mapped root toplevel — dropping" | Chain onto the popover's parent window, offset by the popover anchor + its xdg geometry |
| 14 | Any client could restyle the hovered window's cursor | `request_set_shape` not gated to the pointer-focused seat client | Same check as sway/labwc |
| 15 | Wheel over an X11 menu/combo did nothing; right-click on a menu item opened the browser's context menu | popup overlay sent a raw delta with no `notches` (value120 = 0 → no buttons 4/5) and no deltaMode normalization; no `contextmenu`/`pointerdown` preventDefault | Shared `normalizeWheel` (display-frames.ts) for both paths; preventDefault on the overlay |

Also folded in: `popup_destroy` pops the grab stack (destroy-without-unmap left a
dangling surface); XDG `request_maximize` mirrors the state to the wash window so
the frame maximizes with the guest; override-redirect windows are no longer
re-configured to their own geometry at map (a no-op round trip sway/labwc don't do).

## Cleared (checked against reference, correct as written)

`wlr_seat_pointer_notify_axis` units (value120) and the vkbd → seat key path
(tinywl pattern); xdg-decoration `set_mode` before the initial commit (what sway
1.9/labwc 0.7.4 do on 0.17); the initial-commit `set_size(0,0)` (redundant on
0.17.4, harmless); nested popup offset math (Σ popup geometry − leaf xdg geometry);
`WLR_SCENE_DISABLE_VISIBILITY` exists in 0.17.4; `SIGPIPE` is ignored in
`main.cpp`; the blocking wire write path (drops happen router-side and recover
via `window.force_frame`); the unmap → dissociate → destroy triple teardown is
idempotent; router focus ordering always sends unfocus(prev) before focus(new).

## Tests

- `e2e/tests/display-x11-probe.spec.ts` + `tools/display-x11-probe.c` (xcb, no
  toolkit): untyped override-redirect → overlay **without** grab and parent input
  still reaches the parent (#3); typed MENU + transient-for → grab, parented
  through the chain (#7); `_NET_WM_STATE_FULLSCREEN` → the client receives a
  screen-sized ConfigureNotify (#6); atoms interned on `ready` (#2).
- `display-guest.spec.ts`: both backends now assert `stored 21 bytes` (the
  sentinel) — the X11 leg was previously not asserted at all (#1).
- `web/shell/src/display-frames.test.ts`: `SerialQueue` ordering under a slow
  first step + error tolerance (#5); `normalizeWheel` line/page/notch cases (#15).
- Red-to-green: against the pre-fix compositor binary the guest clipboard legs
  and all three probe tests fail; against the fixed one the whole display set
  (30 specs incl. settings Display) is green under `WLR_RENDERER=pixman`.

## Pixman spike (docs/DISPLAY_E2E.md §2.1) — PASSES

With stock Ubuntu 24.04 wlroots 0.17.1 and `WLR_RENDERER=pixman`: "Creating
pixman renderer", Xwayland up (glamor → software), xclock streams a full
166×166 first frame, browser canvas 99% non-blank, real-tier specs green in
under a second each. The P0 CI gate in DISPLAY_E2E.md is buildable now.
