# wash-display Phase-H features — implementation guide

Passes 1–2 of the 2026-07-01 correctness reviews are COMPLETE: every confirmed
correctness bug is fixed and merged to local `main`, plus the tractable Phase-H
protocol gaps (H2 primary selection, H3 viewporter, H7 request_minimize) and
the three wire-additions (F5 head-steal banner, G4 min/max clamp, G2 keymap
hint). The retired review docs and their resolution tables live in git history
(deleted 2026-07-03 in the docs consolidation); `TODO.md` §Display tracks what
remains — this file is the implementation guide for those items.

**This pass is NET-NEW FEATURES, not bug fixes** — the remaining X11/Wayland
protocol-coverage gaps. Each is additive and independently shippable;
**confirm scope/priority with the user before starting each** (this is the
phase most likely to be trimmed). Do the items in the order below
(value-ranked); do NOT start one not listed here without asking.

## Ground rules (unchanged from pass 2 — do not skip)

1. **One worktree branch per item** under `branches/` (e.g.
   `git worktree add branches/feat-dnd-h1 -b feat-dnd-h1`); do the work there,
   merge to local `main` when green, then `git worktree remove` it. `e2e/` is
   NOT a pnpm workspace member — in a fresh worktree run
   `pnpm install --ignore-workspace` + `pnpm exec playwright install chromium`
   inside `e2e/`, and `make wash-display` for the compositor. **Your shell cwd
   does not follow Edit/Write paths** — after `git worktree add`, explicitly
   `cd` into the worktree before running `go`/`make`/`pnpm`, or you'll build
   `main` instead of your branch (this bit pass 2).
2. **Green gate before merge**: `make unit-test`, `make test-race` (router/
   loopback are the race-sensitive packages; the full ‑race suite may exceed a
   10-min cap under load — verify the changed packages with a targeted
   `go test -race -run …`), and `make e2e-test`. Never pipe a long build/test
   through `| tail`/`| head` (SIGPIPE masks the exit code). The wash-display
   e2e flakes in the FULL suite (concurrent compositors contend) but passes in
   isolation — verify any display failure with `--workers=1` after
   `pkill -x wash-display; pkill -x Xwayland` + `make wash-display`.
3. **One feature = one commit** (or a few sub-commits for H1), message
   `feat(<component>): <what>`.
4. **Each feature gets a test where automatable.** Compositor-side Wayland
   protocol behavior has NO C++ unit harness — its automated coverage is the
   `e2e/tests/display-*.spec.ts` specs (real GTK/Qt/X11 guests). Router/FE
   halves ARE unit-testable (Go + vitest/node:test) — test those. State any
   manual-only verification in the commit and add it to the checklist below.
5. **CBOR pitfall**: never introduce `json.RawMessage`/`[]byte` in structured
   BE→FE fields.
6. If a feature turns out larger/riskier than described, **stop and ask**.

### The two wire-addition patterns established in pass 2 (reuse them)

- **New BE→FE ctrl message** (e.g. `shell.superseded`): add (a) the `T` constant
  in `internal/wire/msgs_shell.go`, (b) the struct, (c) a `New*` constructor
  (NO `DecodeCtrl` case needed — BE→FE only, FE JSON-parses); then FE (d) an
  interface with the literal `t`, (e) add it to the `ShellCtrlMsg` union, (f) a
  `case` in `makeHandlers`'s `onCtrl` switch (`web/shell/src/main.tsx`).
- **New app→router event** (e.g. `window.state`): add (a) the `T` const + struct
  in `internal/wire/msgs_event.go`, (b) a dispatch `case` + handler in
  `internal/router/app_session.go` (gate with `inst.ownsWindow(win)`), (c) a
  `WireConn::report_*` method in `cpp-sdk/wash/wire_conn.{hpp,cpp}` that writes
  the event on `CH_EVENT`.

---

## Phase H-feat items (value-ranked)

### H1 — wl_data_device drag-and-drop (HIGHEST value, LARGEST) — branch `feat-dnd-h1`
In-app drag is dead: `seat->events.request_start_drag` is unhandled, so GTK
text drags, Qt file-view drags, and Chromium tab-tear gestures do nothing.
Likely 3 sub-commits:
1. **Compositor accept-drag**: add a `request_start_drag` seat listener; on a
   valid grab call `wlr_seat_start_pointer_drag`. Track the drag in the pointer
   input path so injected motion/button drives `wlr_seat_pointer_notify_*`
   during the drag.
2. **FE drag surface**: the drag icon is a `wlr_drag` surface that wash must
   capture + composite at the pointer, like the popover overlay path
   (`toplevel_setup_popover` / `push_popup_grab` in `compositor.cpp`). A new FE
   affordance renders it following the cursor.
3. **Data transfer**: `wl_data_offer` MIME negotiation on drop — reuse the
   clipboard selection machinery (`handle_set_selection` fd-pump pattern, now
   poll-bounded) for the drag payload.
- **Test**: e2e with a real GTK/Qt app that drags between two views; manual for
  the visual drag feel. **Stop and ask** before adding FE drag-surface wire
  messages — this is where scope can balloon.

### H5 — xdg-activation ("focus me" / URL handoff) — branch `feat-activation-h5`
`wlr_xdg_activation_v1_create` is absent, so activation requests are dropped.
- Compositor: create the manager; add a `request_activate` listener
  (`events.request_activate`, event has `{surface, token}`). Resolve
  `surface → wash win` via `g_win_reg` and ask the router to focus/raise it.
- **New app→router event needed**: there is NO app-initiated focus event today
  (`window.focus` is shell→router and router→app). Add `window.activate`
  (app→router) → router calls the focus path (`winSession.focus` +
  `broadcastPatches`) gated to `ownsWindow`. Add `WireConn::report_activate`.
- **Test**: router unit (an owned window's activate → focused patch); manual
  for a real "raise me" from a guest.

### H7-rest — set_app_id + per-app icons, parent stacking — branch `feat-appid-parent-h7`
Two independent halves:
- **set_app_id → icon**: listen `xdg_toplevel->events.set_app_id`; add
  `WireConn::report_app_id(win, app_id)` → new app→router `window.app_id` event.
  Router maps app_id → an icon (or forwards it to the FE, which resolves it).
  Cosmetic; FE renders the per-app titlebar/taskbar icon.
- **parent stacking**: `create_window` already accepts a `parent` param (pass 2
  left `sink_open` passing 0). In `toplevel_map`, resolve
  `t->xdg_toplevel->parent → parent Toplevel → parent->sink.win` (the popover
  path already does `tl->parent->base->data`) and pass it through `sink_open` →
  `create_window`. `EvtWindowCreate.ParentWin` already exists; thread it into
  `SessionWindow` (new `ParentWin` field) and have the FE keep a child's `gz`
  above its parent's so titled dialogs stack above their owner.
- **Test**: router unit (parent threads onto SessionWindow); manual for icons +
  stacking.

### H4 — presentation-time — RECOMMEND SKIP (document as won't-do)
Low value: wash captures surfaces directly (not the scene — `WLR_SCENE_DISABLE_
VISIBILITY=1`), so real presentation feedback isn't cheaply derivable, and
Chromium/Firefox already fall back to frame callbacks fine. Half-advertising the
global risks mpv waiting for feedback that never arrives. Only revisit on a
concrete A/V-sync complaint. Record in `TODO.md` won't-do unless the user wants
it.

---

## Explicitly OUT of scope / won't-fix

- **H6 Xwayland `-auth`** — WON'T FIX (decided 2026-07-03). Inherited wlroots
  limitation (shared with sway), not an Xwayland bug; only exploitable under
  shared-host-shared-netns multi-user and NOT closeable by a compositor patch
  (one X socket is abstract/netns-global). Mitigable wash-side via per-user
  network-namespace isolation at the privileged spawn layer IF that deployment
  ever materializes — that's a login/spawn feature, not a display fix.
- Nested serial-less Qt submenu chaining (`TODO` in `toplevel_setup_popover`) —
  low value; the primary popover classifier already works for the common case.
- Popover classifier over-match (#5) — investigated twice, no robust menu-vs-
  dialog signal exists; a TITLED dialog already stays a window. Leave it.
- Touch input — the FE only synthesizes pointer events.

---

## Manual-verification checklist (carried from passes 2–3; need a human at a display)

Automated gates can't cover these — run each once and report:
- **G2 keymap**: from a non-US host layout (AZERTY/QWERTZ) typing into an
  Xwayland/Wayland guest produces the right characters.
- **G3 selection leak**: a guest that claims the clipboard selection then exits/
  hangs without writing leaves no leaked reader thread/fd.
- **G4 min/max**: a Qt app with a hard minimum resizes without rubber-banding;
  a fixed-size window can't be stretched.
- **H2 primary selection**: middle-click paste works between two guests and
  to/from an X11 client.
- **H3 viewporter**: an mpv/SDL2 fractional client renders crisply.
- **H7 minimize**: a guest's CSD minimize button hides the wash window.
- **H1/H5/H7-rest**: the drag/activate/icon/stacking behaviors added here.

When an item merges green, delete its `TODO.md` line (the merge commit is the
record; resolved items are not kept as history).

## Reference: code landmarks (mapped in pass 3 — start here, don't re-discover)

**Compositor** (`wash-display/src/compositor.cpp`, ~2500 lines):
- `run_compositor` init chain: `wlr_compositor_create` → `wlr_data_device_
  manager_create` → **new global managers go right here** → `wlr_xdg_shell_
  create` → seat block (`wlr_seat_create`, vkbd, selection + primary-selection
  listeners) → Xwayland block (`#ifdef WASH_DISPLAY_XWAYLAND`) → output → run.
- `Server` struct (~line 165): add new manager ptrs + `wl_listener`s here.
- `Toplevel` struct (~line 310) + `server_new_xdg_toplevel` listener wiring
  (~line 1260) + `toplevel_destroy` `wl_list_remove` block (~line 787) — any
  new `xdg_toplevel->events.*` listener needs all three.
- Cross-thread marshalling: reader thread pushes `WinCmd` onto `g_cmds` (+ a
  `std::string s` field for string payloads) and writes the self-pipe;
  `on_cmd_pipe` → `apply_win_cmd` drains on the compositor thread. `post_display_
  dpi`/`post_display_keymap` are the templates for "FE setting → compositor".
- `apply_keymap` (keymap rebuild), `sink_open` (mints the window via
  `create_window`, now carries min/max), `handle_set_selection` (fd-pump,
  poll-bounded — the DnD data-transfer template).
- `main.cpp` `on_app_msg` (~line 247): FE→compositor app_msg dispatch
  (`display.set_dpi/metrics/keymap`, `input`); add new FE→compositor kinds here.

**cpp-sdk** (`cpp-sdk/wash/wire_conn.{hpp,cpp}`): `report_title` /
`report_window_state` are the templates for a new compositor→router report;
`create_window` sends the request/reply with min/max + parent params.

**Router**: app-event dispatch switch in `internal/router/app_session.go`
(~line 344); `ownsWindow` gate; `relayWindowGeometry`/`relayWindowState`
handlers; `winSession` helpers in `wmstate.go` (`createWindow`, `setState`,
`resize`, `focus`); `SessionWindow` struct in `internal/wire/msgs_shell.go`.

**FE** (`web/shell/src/`): `main.tsx` `makeHandlers`→`onCtrl` `switch(msg.t)`
(~line 386) is the ctrl-message dispatch; `ShellCtrlMsg` union (~line 266);
`ConnectionBanner` (~line 1200) is the persistent-banner template;
`publishDisplayMetrics`/`publishKeymap` (~line 720) are the "FE→compositor
app_msg" senders; `wash-app-display.ts` `applyResize` (min/max clamp) +
`onKeyDown`/`sendInput` (input path) + control-frame handler.
