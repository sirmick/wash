# wash-display — X/Wayland app surfaces as wash windows

Status: **design** (2026-05-29). This document is the plan of record for running
native Linux GUI apps (Wayland and X11) inside wash, each app window appearing as
a first-class wash window, with pixels encoded host-side and streamed to the
browser shell.

It is deliberately staged so the gating interface (the wire contract) lands and is
tested long before any compositor C++ exists. See [§9 Commit ladder](#9-commit-ladder).

Related: [WIRE.md](WIRE.md), [ARCHITECTURE.md](ARCHITECTURE.md), [INGRESS.md](INGRESS.md)
(the other "embed a foreign UI in a wash window" path — ingress is for web apps,
this is for native GUI apps).

---

## 1. Goal & non-goals

**Goal.** A user launches GIMP / Firefox / xterm; each top-level window shows up as
a normal wash window (drag, resize, z-order, focus all handled by wash's existing
WM); keyboard/mouse/scroll work; copy-paste bridges to wash's clipboard. Both
Wayland-native and X11 apps are supported.

**Non-goals (v1).** GPU passthrough / 3D acceleration to the guest; audio (separate
service); drag-and-drop *between* a native window and a wash app; running this
inside the riscv emulator VM (host-native only — the VM framebuffer is a separate,
simpler future track); HiDPI per-monitor fractional scaling beyond a single
device-pixel-ratio.

---

## 2. The architecture in one picture

```
browser (wash shell, Solid)
  └─ <wash-app-display> web component   ← ported from mac-phoenix client.js, ONE per display window
        ▲ video (raw "video" channel and/or WebRTC)      ▼ input (app_msg, FE→own-BE)
┌──────────────────────────────── host ────────────────────────────────┐
│  wash-router (Go, existing)  —  fork+exec + fd3 + framed JSON          │
│      treats wash-display as an ordinary background app                 │
│        │ spawn                                                          │
│        ▼                                                                │
│  wash-display (NEW, C++ — optional compile, separate package)          │
│     ├─ libwlroots .......... compositor core + xdg-shell + wlr_xwayland │
│     ├─ encoders ............ COPIED from mac-phoenix (H264/VP9/PNG/WebP)│
│     ├─ transport ........... COPIED from mac-phoenix (libdatachannel/WS)│
│     ├─ wash-wire client .... NEW, small (fd3 frame codec + JSON)        │
│     ├─ spawns ► Xwayland (system binary)                                │
│     └─ spawns ► gimp / firefox / xterm … (unmodified apps)              │
└────────────────────────────────────────────────────────────────────-──┘
```

**Front-half** (compositor, Xwayland, per-surface capture) — *link* `libwlroots`;
it gives us xdg-shell surfaces and, crucially, `wlr_xwayland` (the X11 window-manager
shim) so X11 apps arrive as Wayland surfaces on the same path. **Back-half** (encode,
transport, browser client) — *copy large sections* from the user's own
`github.com/sirmick/mac-phoenix`: a lean libdatachannel + WS multi-codec pipeline and
a vanilla-JS client. No GStreamer, no AGPL entanglement.

The integration seam is mac-phoenix's frame push, shaped like
`send_video_frame(buf, size, is_keyframe, w, h, dirty_x, dirty_y, dirty_w, dirty_h, frame_w, frame_h)`.
A wlroots per-surface damaged buffer maps onto exactly this. **Swap the frame
producer, keep everything downstream.** wash-display is single-process in v1
(threads for encode); the Sommelier-style parent + per-app-child split is a later
internal refactor, not more wash-visible binaries.

---

## 3. Why this is mostly additive to wash

| Concern | Mechanism | New? |
|---|---|---|
| Language | router is language-agnostic: fork+exec + fd3 + framed JSON. A C++ app does `identity` and is a first-class app. | reuse |
| One process → many windows | new `window.create/created/destroy` events (mirror `spawn.request`) | **NEW (wire)** |
| Per-window pixels | `OpenChannel(win, kind="video")` — channels are already window-bound | reuse + 1 kind |
| Input (mouse/key) | `app_msg` FE→own-BE; router never parses it | reuse (app-private) |
| Clipboard | existing `clipboard.set/get/data/changed` | reuse |
| Window geometry/z/focus | router stays the WM authority; shell renders `SessionWindow` list unchanged | reuse |
| Capability gate | new `windows` capability (mirror `spawn`) | **NEW (wire)** |
| Optional compile | build tag + Makefile GOARCH gate (mirror `wash-sudo`) | pattern reuse |
| Separate package | extra deb/rpm/apk package depending on wayland/x11 libs | **NEW (packaging)** |

Net new wire surface: four `window.*` events, one channel kind, one capability. The
shell needs **no** changes — it already renders a list of `SessionWindow`s and doesn't
care that several share an `instance_id`.

---

## 4. The multi-window contract (the gating interface)

Today the router mints one window per app instance at `identity.ack`. wash-display
owns *N*, created dynamically. We add channel-1 `Evt*` events modelled exactly on the
spawn protocol (`req_id` correlation, `*.err` reply).

```jsonc
// app → router
{ "t":"window.create", "req_id":7, "role":"toplevel",   // | "popup"
  "parent_win":0,                                        // set for popup/transient
  "title":"GIMP", "w":1024, "h":768,
  "min_w":320, "min_h":240, "max_w":0, "max_h":0 }
// router → app
{ "t":"window.created", "req_id":7, "win":42 }
{ "t":"window.create.err", "req_id":7, "code":"forbidden", "msg":"windows capability not declared" }
// app → router
{ "t":"window.destroy", "win":42 }
```

- The router allocates `win`, creates a `SessionWindow{instance_id:<wash-display>, ...}`,
  and emits the existing `session.patch`→`window.upsert` to the shell. Destroy →
  `window.delete`.
- All existing router→app window events (`window.mapped/focus/unfocus/resize/state/
  close_requested`) and `window.set_title` already key on `Win uint32` — for a
  multi-window instance they simply name *which* of its windows. No struct change
  needed; only the router's routing learns "deliver to the instance that owns this
  win," and the app learns to demux by `win`.
- `role` + `parent_win` is where Wayland `xdg_popup` / X11 override-redirect map in:
  the shell already has the parent geometry, so a popup renders borderless, positioned
  relative to its parent.
- Gated by the new **`windows`** capability so an arbitrary app can't spawn unbounded
  chrome (same trust model as `spawn`).

This contract is reusable by any future multi-window app (a real browser, an IDE),
not just display. **Land and test it first** ([commit 2](#9-commit-ladder)).

---

## 5. Per-window video

Each surface gets one raw channel opened with a new kind:

```go
OpenChannel(ctx, win, kind="video")   // ChannelKindVideo
```

- **Image codecs (PNG/WebP)** ride the raw channel in-band through the router, framed
  with mac-phoenix's `[8-byte ready-ts | dirty-rect | 45-byte meta | payload]` header.
  Simple; works through any proxy; the natural first path for the e2e.
- **Video codecs (H.264/VP9)** want a **WebRTC side-channel**: SDP/ICE are signalled
  *through* the router as `app_msg`s (FE↔BE), but media flows browser↔wash-display over
  UDP/RTP — giving jitter buffering and congestion control a TCP-over-WS channel can't.
  mac-phoenix already implements both halves; we keep its `CodecFallbackController`
  (vp9 → h264 → webp → png → httpstream).
- `kind="video"` lets the shell mount the `<wash-app-display>` decoder component
  rather than treating the channel as opaque bytes.

---

## 6. Input (keyboard / mouse)

A display window is a pixel canvas, so raw input must be forwarded to wash-display and
injected into the real surface — unlike normal wash apps whose FE handles its own DOM
events. **This needs no router/wire changes**: the display FE and wash-display BE talk
over `app_msg` (own-FE → own-BE, neither `from`/`to` set; router relays opaquely).

```jsonc
// <wash-app-display> FE → wash-display BE, via app_msg {kind:"input", ...}
{ "kind":"input", "win":42, "events":[
   {"ev":"motion","x":312,"y":88},            // surface-relative ints
   {"ev":"button","btn":"left","state":"down"},
   {"ev":"axis","axis":"v","delta":-120},
   {"ev":"motion_rel","dx":4,"dy":-2},         // pointer-lock / games
   {"ev":"key","code":"KeyA","state":"down"}
]}
```

- **Coalesce + batch** motion to one `app_msg` per `requestAnimationFrame`; reuse
  mac-phoenix `cachedMouseScaleX/Y` for canvas→surface mapping (× devicePixelRatio).
- **Priority class.** Send input frames `ClassInteractive`, video `ClassBulk`
  (`wire/frame.go`). The router's priority classes keep cursor/keys responsive even
  while a window floods video — a property Sommelier had to engineer by hand.
- **Keyboard.** FE sends DOM `KeyboardEvent.code`; wash-display maps `code`→xkb keysym
  and drives a wlroots virtual keyboard with an xkb keymap. Discrete down/up only — the
  compositor/app owns repeat. IME / dead keys deferred.
- **Focus is router-authoritative.** On `window.focus{win}` wash-display sets `wl_seat`
  keyboard focus to that surface; pointer enter/leave derives from motion. Because wash
  owns focus, we skip the dual-connection focus-sync round-trips Sommelier needed.

---

## 7. Copy / paste

wash's clipboard is **eager + router-held** (`clipboard.set` stores bytes & broadcasts
`clipboard.changed`; `clipboard.get`→`clipboard.data` with `req_id`). Wayland/X11
selections are **lazy + owner-served**. wash-display bridges both directions reusing
the existing vocabulary — **zero wire changes**.

- **guest → wash** (app copies): client takes the Wayland selection (or X11 `CLIPBOARD`
  via Xwayland) → wash-display reads `text/plain` (and `image/png`) eagerly and calls
  `clipboard.set`. v1 is eager; large-blob laziness is a noted extension.
- **wash → guest** (paste into app): `clipboard.changed(mime)` → wash-display claims the
  Wayland selection + X11 `CLIPBOARD`/`PRIMARY`, advertising mimes *without* pulling
  bytes. On actual paste, the compositor asks the owner → wash-display does
  `clipboard.get` → writes bytes to the client's `wl_data_offer` fd. Lazy, fits the
  req/reply perfectly.

Inherited gotchas (from the Sommelier study): write to **both** X11 `PRIMARY` and
`CLIPBOARD` and the Wayland primary + regular selections; negotiate a small MIME set
(`text/plain`, `image/png`) against wash's single entry.

**Noted extensions, not v1:** a second wash clipboard slot for primary-selection
(middle-click paste); a lazy clipboard-owner mode so the guest→wash path doesn't copy
unpasted blobs; cross-window DnD (achievable in wash precisely because the router knows
window geometry — impossible for Sommelier under sway).

---

## 8. Optional compile & packaging

The compositor links wlroots/wayland/X11 — native libs the pure-Go core must never
pull in. Two hard guarantees:

1. **`CGO_ENABLED=0` core is untouched.** wash-display is a separate *native* binary
   that talks to the router only over the wire — there is no Go import edge, so the
   core `go.mod`/binaries stay cgo-free and riscv-buildable automatically.
2. **Built only on opt-in, never on riscv.** Mirror the `wash-sudo` pattern in the
   Makefile:

   ```make
   # opt-in; never on the emulator target
   WASH_DISPLAY ?=
   ifeq ($(WASH_DISPLAY),1)
   ifneq ($(GOARCH),riscv64)
   DISPLAY_TARGETS := $(OUT)/wash-display
   endif
   endif
   # wash-display is built by wash-vm-display/ (CMake); the Makefile just invokes it
   $(OUT)/wash-display: | $(OUT)
   	$(MAKE) -C display && cp display/build/wash-display $@
   ```

   The thin Go-side glue (FE bundle embed + manifest probe, if we use a Go wrapper at
   all) goes behind `//go:build wash_display` and a `cmd/wash/imports_display.go`
   blank-import, exactly like `imports_apptest.go` — so the multicall binary excludes
   it unless `-tags=wash_display`.

**Separate package.** The main `wash` package stays dependency-clean; a new
`wash-display` package depends on `wash` + the native libs:

- **debian** — new stanza in `debian/control`:
  ```
  Package: wash-display
  Architecture: amd64 arm64
  Depends: wash (= ${binary:Version}), libwayland-server0, libxcb1, xwayland, libwlroots12
  Description: native X/Wayland app surfaces for wash
  ```
  plus `debian/wash-display.install` → `out/wash-display usr/bin`.
- **rpm** — `%package display` + `%files display` in `rpm/wash.spec`, `Requires: wash = %{version}-%{release}, wayland, libxcb, xorg-x11-server-Xwayland, wlroots`.
- **alpine** — a subpackage in `alpine/APKBUILD` (`subpackages="wash-display:_display"`),
  `depends="wash=$pkgver wayland libxcb xwayland wlroots"`.

---

## 9. Commit ladder

Each row is one reviewable commit. Commits 1–3 are pure Go / docs / tests and land
**before** any C++ — they make the contract real and regression-tested. Commits 6+
need a box with wlroots installed and are gated out of default builds.

| # | Commit | Touches | Buildable/testable in CI now? |
|---|---|---|---|
| 1 | `docs: wash-display design (DISPLAY.md)` | docs | ✅ (this commit) |
| 2 | `wire: window.create/created/destroy + video channel kind + windows cap` | `internal/wire`, `internal/sdk` (cap mirror) | ✅ `go test ./internal/wire/...` |
| 3 | `router: multi-window per instance (handle window.create/destroy, route window.* by win)` | `internal/router` | ✅ router unit tests |
| 4 | `sdk: Conn.CreateWindow/DestroyWindow + per-win event demux` | `internal/sdk` | ✅ sdk tests |
| 5 | `apptest+e2e: one BE drives 2 windows, streams a canned PNG per window` | `apps/test`, `e2e`, minimal `<wash-app-display>` | ✅ Playwright + router-log (see §10) |
| 6 | `display: C++ skeleton — manifest probe, fd3 frame codec, identity handshake` (stub, no wlroots) | `display/` | gated `WASH_DISPLAY=1` |
| 7 | `display: wlroots compositor core + xdg-shell → window.create per toplevel` | `display/` | needs wlroots |
| 8 | `display: per-surface capture → mac-phoenix encoders → video channel` | `display/` | needs wlroots |
| 9 | `display: input injection (wl_seat virtual pointer/keyboard) from app_msg` | `display/` | needs wlroots |
| 10 | `display: Xwayland + wlr_xwayland XWM → X11 windows as surfaces` | `display/` | needs wlroots+Xwayland |
| 11 | `display: clipboard bridge (wl_data_device + X11 selections ↔ clipboard.*)` | `display/` | needs wlroots |
| 12 | `packaging: separate wash-display deb/rpm/apk; Makefile WASH_DISPLAY gate` | `debian/`,`rpm/`,`alpine/`,`Makefile` | ✅ (no-op unless opted in) |

The value of this ordering: **commit 5 proves the entire wash-visible contract**
(multi-window create, per-window video channel, input round-trip, clipboard) with a Go
fake and a browser — no compositor required. If commit 5 is green, the C++ work in 6–11
plugs into a contract that's already known-good.

### 9a. Compositor build reality (as built)

The compositor half (commits 7–8) is built and runtime-verified. Notes that diverge
from the original sketch above:

- **wlroots: linked from the system today; vendored 0.17.4 source is staged for a
  future static build.** `wash-display/CMakeLists.txt` currently locates wlroots via
  `pkg-config` — it tries `wlroots-0.17` (Fedora/Arch) and falls back to the bare
  `wlroots` (Ubuntu 24.04 = 0.17.1). So the build prerequisite **as built** is the
  distro `libwlroots-dev` (0.17.x) plus the system graphics `-dev` libs (wayland-server,
  xkbcommon, pixman, libdrm, gbm, egl, glesv2, libinput) and `wayland-protocols` +
  `wayland-scanner`. `WASH_DISPLAY=1 make` is the entry point.
  - A pinned copy of wlroots **0.17.4** is committed as source under
    `wash-display/third_party/wlroots/` (see its `PROVENANCE.md`). It is the *intended*
    static-build target but is **not yet wired into CMakeLists** — nothing in the build
    references it today. Wiring it up (Meson sub-build → `libwlroots.a`) is tracked
    work, not the current reality; until then the vendored tree is reference source.
  - Why pin 0.17.4 rather than rely on the distro: stock Ubuntu 24.04 ships a frozen
    **0.17.1** that lacks the GPU pixel read-back we need (`wlr_renderer_read_pixels`
    on dmabuf clients); 0.17.4 has it. (0.18 was tried and rejected — it requires
    `wayland ≥1.23` but the platform has 1.22.0, and faking that means patching
    wlroots' generated shm protocol glue. 0.17.4 builds clean against system
    wayland 1.22.) Pinning one version also avoids per-distro `.pc`-name games and
    0.17-vs-0.18 API forks in our code. **Caveat for contributors:** on a stock
    Ubuntu 24.04 box the system `libwlroots-dev` is 0.17.1, so GPU read-back capture
    may be degraded until the vendored 0.17.4 static build lands.
- **Headless backend.** No real output device — the right fit for a streaming
  compositor. One virtual output; clients see a normal display.
- **Capture is GPU-capable.** Per-surface capture goes
  texture → pooled render-target → `wlr_renderer_begin_with_buffer` (bind FBO) →
  `wlr_renderer_read_pixels` → CPU buffer. This captures hardware-accelerated
  (dmabuf) clients, not just `wl_shm` ones. Gotchas: read back as `XBGR8888`
  (`GL_RGBA`, universally supported) and swap R↔B for the WebP `BGRA` encoder —
  `XRGB8888`/`GL_BGRA_EXT` is not guaranteed on llvmpipe/surfaceless; and the gbm
  allocator needs an explicit `DRM_FORMAT_MOD_LINEAR` modifier for a CPU-readable BO.
- **Threaded wire client.** `WireConn` runs one reader thread; `window.create` /
  `channel.open` are blocking request/reply via per-`req_id` condvars (must never be
  called from the inbound `app_msg` callback — that deadlocks the reader). JSON is
  nlohmann/json (vendored). WebP rides the raw video channel framed with the 45-byte
  little-endian mac-phoenix WS header (§5; codec inferred from payload magic bytes).
- **Discovery + spawn.** On startup the compositor sends `app_msg{kind:"display_ready",
  wayland_display:"wayland-N"}` to the router, and (if `$WASH_DISPLAY_EXEC` is set)
  fork+execs that guest app with `WAYLAND_DISPLAY`/`XDG_RUNTIME_DIR` pre-set so it
  connects straight to this compositor.
- **CI-free verification.** The contract e2e (commit 5) runs in CI without a
  compositor. The compositor itself is proven by a local smoke harness in `tmp/`
  (gitignored): a fake router + the headless compositor + a minimal xdg_toplevel
  client → asserts toplevel→`window.create` and capture→WebP→video-channel
  end to end. Not in default `make e2e` (needs wlroots + a GL/EGL device).

---

## 10. E2E test plan

Following the wash e2e pattern ([memory: wash e2e pattern] — test app + Playwright FE +
router-log BE assertions), we test the *contract*, not wlroots. CI has no compositor, so
the "frame producer" is faked in Go.

**Fixture.** A `display` mode in the existing hidden test app (`apps/test`), declaring
`Surface=background` + `CapWindows`. Driven via `router.sendAppMsg`:

- `{"kind":"display.open","n":2}` → the test BE calls `CreateWindow` twice, then
  `OpenChannel(win, kind="video")` for each, and writes one canned PNG frame
  (solid color + a 1px marker) per channel using mac-phoenix's WS frame header.
- A minimal `<wash-app-display>` web component (the PNG path of the ported client.js)
  mounts in each window and `putImageData`s the frame.

**Assertions.**
- *BE (router log):* `waitForLog(/window\.create .*req_id=/)`, `/window\.created win=/`
  twice, `/channel\.open .*kind=video/` twice. A `windows`-capability denial test: an
  app *without* the cap gets `window.create.err code=forbidden` (assert the log line).
- *FE (Playwright):* two `wash-app-*` windows appear; each window's `<canvas>` has
  non-blank pixels at the marker location (`page.evaluate` reads `getImageData`).
- *Input round-trip:* Playwright clicks at a known canvas coord; assert the test BE
  logged the decoded `app_msg{kind:"input"}` with the expected surface-relative `x/y`
  (proves the FE→BE input path + coordinate mapping without needing a real surface).
- *Clipboard:* test BE `clipboard.set`s; a second app observes `clipboard.changed`;
  reverse — set from the shell, assert test BE receives `clipboard.changed` and a
  `clipboard.get` returns the bytes.

**Orphan hygiene.** [memory: e2e orphan accumulation] — the display test spawns extra
children (windows are cheap, but fixtures aren't); the test tears down the instance and
the fixture asserts the child count returns to baseline.

A later, non-CI smoke test (commit 10+) runs real `weston-info`/`xterm` against a built
wash-display on a wlroots-equipped runner; kept out of the default `make e2e`.

---

## 11. Open questions / risks

- **Buffer churn** (from the Sommelier study): Xwayland can create ~100 short-lived shm
  pools/sec during scroll. wash-display must pool/reuse buffers from day one — design
  the capture path around reuse, not per-frame alloc.
- **Multi-window routing in the router** (commit 3) is the first place the
  one-instance-one-window assumption is challenged; audit `byWin`/instance teardown so
  destroying one window doesn't tear down the instance, and instance death GCs all its
  windows + video channels.
- **WebRTC negotiation through the router**: confirm `app_msg` is an acceptable carrier
  for SDP/ICE (size, ordering) or whether a dedicated signalling channel kind is cleaner.
- **Security**: a window-spawning capability is a chrome-DoS vector; consider a per-
  instance window cap (count limit) enforced router-side.

---

## 12. Interactivity milestones (input / clipboard / native polish)

The compositor in §9a streamed pixels but was **view-only** — commits 9 (input) and
11 (clipboard) were never built, so windows received no mouse/keyboard and the
clipboard didn't bridge. This section tracks closing that gap. Each milestone is
test-gated (the contract e2e runs without a compositor; a real-stack smoke uses
`out/wash-display` + `xclock`, gated out of default `make e2e`).

### M0 — input/clipboard contract fixtures ✅
`apps/test/be` decodes + logs `app_msg{kind:"input"}` per event (routing by the
**payload** `win`, since cross-instance app_msgs land on the instance's primary
window). `e2e/tests/display.spec.ts` asserts the §6 input contract (control-socket
driven) and the §7 clipboard contract (two instances; `clipboard_get` echoes its
`id` so the control socket correlates the reply). CI-green, no compositor.

### M1 — input: seat, pointer, keyboard, focus ✅
- `compositor.cpp` creates `wlr_seat` + a virtual keyboard with a default xkb keymap
  — which also **fixes the latent NULL seat** previously handed to
  `wlr_xwayland_set_seat` (Xwayland's keyboard would have aborted under load).
- Pointer (`enter`/`motion`/`button`/`axis`/`frame`) and keyboard injection, resolved
  `win → surface` via `g_win_reg`. Keys go through the virtual keyboard
  (`wlr_keyboard_notify_key`) so xkb modifier state is correct (the tinywl pattern).
- Input is marshalled from the WireConn reader thread onto the compositor thread over
  the existing self-pipe (`g_cmd_pipe`), alongside resize/close.
- `window.focus`/`window.unfocus` are now dispatched in `cpp-sdk/wash/wire_conn.cpp`
  and applied as seat keyboard focus (router-authoritative — DISPLAY.md §6).
- FE `web/shell/src/wash-app-display.ts` captures pointer/key/wheel, coalesces motion
  to one batch per `requestAnimationFrame`, maps to surface-local coords (DPR 1.0),
  and sends to the owning instance via `window.wash.sendAppMsgTo`. Right-click and
  browser shortcuts are suppressed while a guest is focused. `motion_rel` (pointer
  lock) is deferred.
- **Verified** end to end by `display-input-smoke.spec.ts`: clicking + typing on a
  real `xclock` window's canvas is injected into the live wlroots surface.

### M2 — clipboard bridge ✅
Reuses wash `clipboard.*` (no new wire). `cpp-sdk` `WireConn` gains
`clipboard_set`/`clipboard_get` (req/reply) + an `on_clipboard_changed` hook, with
base64 for the byte field (matches the Go SDK's `[]byte` wire encoding — see
[[wash_cbor_json_pitfall]]). The compositor bridges: `request_set_selection` accepts a
Wayland client taking the selection; `set_selection` mirrors any guest selection
(incl. X11 via the xwm bridge — automatic now the seat exists) into wash; and
`clipboard.changed` installs a lazy `wlr_data_source` that serves wash's bytes to a
pasting guest on demand. Loop-safe (own source skipped; the router excludes the setter
from the broadcast). PRIMARY (middle-click) selection is the deferred **M2b**.
*Remaining verification:* real-app copy/paste smoke needs a clipboard-capable client
(xterm / a Wayland app); the wash-side wire is covered by the M0 contract test.

### M3 — popups / override-redirect ✅
Real apps' menus/dropdowns/tooltips. Done **display-local** — NOT the cross-cutting
WM change first feared. A popup is not a wash window: it streams to its parent
window's `<wash-app-display>` over a new **additive** channel kind
`ChannelKindVideoPopup="video-popup"` (the router relays the kind opaquely —
`handleChannelOpen` passes `m.Kind` through — so zero router/window/patch/WM change),
and the element draws it as a `position:fixed` overlay canvas on `<body>` that can
overflow the window box, forwarding its own pointer/wheel input keyed by the popup
channel (popups have no win → `g_popup_reg`, surface-based). The popup's offset rides
in-band as a sub-45-byte JSON control frame; pixel frames are ≥45 bytes.

- **M3a (Wayland)**: `server_new_xdg_toplevel` now handles the `xdg_popup` role
  (previously dropped); offset from `popup->current.geometry` accumulated up the
  parent chain to the root toplevel.
- **M3b (X11)**: `xsurface_map` branches on `override_redirect` → same overlay path,
  parented to the transient-for window (else the most-recently-mapped toplevel),
  offset = `menu.xy − parent.xy` in X root coords. This is p4v's / Qt-X11's menu path.
- Deferred: popup keyboard focus; the X11 clipboard guest→wash leg rides wlroots' xwm
  X→Wayland selection sync (the wash-side bridge is M2, verified on Wayland).

### M4 — cursor shape forwarding ✅
The guest names its cursor via **cursor-shape-v1**; the compositor
(`wlr_cursor_shape_manager_v1` + `request_set_shape`) forwards the name to the
pointer-focused window's element on its video channel as a sub-45-byte JSON control
frame (`{cursor:"<name>"}`), and the element sets it as the CSS cursor — the
protocol's names ARE the CSS keywords. Recent Xwayland uses cursor-shape-v1, so X11
apps get correct cursors; the browser already shows *a* cursor, so this is shape
fidelity. **M4b** (deferred): bitmap cursors (`request_set_cursor` with a surface)
composited via the reserved WS-header cursor fields.

### M5 — output size / maximize ✅
Virtual output is now **1920×1080** (was 1280×800) so large/maximized apps fit;
client maximize/fullscreen requests are honoured (`request_maximize` /
`request_fullscreen` → sized to the output), and `wlr_xdg_output_manager_v1` gives
clients the real logical screen geometry. **M5b** (deferred): HiDPI — output scale +
`wlr_fractional_scale_v1` + FE devicePixelRatio coordinate scaling in M1; and
`wlr_output_management_v1` for clients that want to *drive* output config (rare).

### M5c — crop to xdg window geometry (CSD shadow margin) ✅
GTK/GNOME apps draw client-side decorations *despite* our forced
`SERVER_SIDE` xdg-decoration (the protocol is advisory; GTK ignores it),
including a wide transparent drop-shadow margin around the visible window.
The capture read-back is alpha-less (`XRGB`), so that margin flattened to a
**black border**. `capture.cpp` now crops the read-back to the surface's
`wlr_xdg_surface_get_geometry` rect (threaded through `sink_frame`,
geometry computed in `toplevel_commit`); the shadow margin never reaches
the encoder, and the captured pixels now match the geometry already
reported to the WM. X11 surfaces and popups keep full-surface capture
(no xdg geometry; no CSD shadow in the X clients tested). Verified by
`e2e/capture/display-probe.cap.ts` (gnome-calculator 482×619 → 360×497,
non-blank 61% → 100%). **Deferred:** preserving alpha end-to-end (ARGB
read-back + WebP-with-alpha + FE transparent compositing) for rounded
corners / genuinely shaped windows — a non-goal while wash wraps every
guest in its own opaque server-side frame.

### Out of scope here — M6 throughput
WebRTC/VP9 (for Firefox scroll/video parity) + audio service hookup are a separate,
larger track ([[wash_display_codecs]], [[wash_audio_plan]]).

### Testing — two layers
Input/clipboard/popups are tested at two levels:

1. **Contract (CI, no compositor)** — `e2e/tests/display.spec.ts` drives the hidden
   test app (`apps/test`), which *fakes* the BE (canned frames, no real surface): it
   proves the wire/FE/router plumbing — the input app_msg shape + coordinate mapping,
   the clipboard `clipboard.*` vocabulary, and the popup overlay/`popup_chan` input
   routing. Runs everywhere.
2. **Real client (needs `out/wash-display` + an app)** — kept out of default `make e2e`:
   - `display-input-smoke.spec.ts`: click + type on a real `xclock` window.
   - `display-guest.spec.ts`: drives `tools/display-testguest.py`, a dependency-free
     **PyGObject GTK3** guest (right-click menu, copy/paste, visible input feedback;
     keyboard triggers `c`/`v`/`m`). One script covers **both** popup paths via
     `GDK_BACKEND` (`wayland` → xdg_popup, `x11` → override-redirect). It's also the
     **manual debug surface**: launch it from a wash terminal and watch input land,
     menus open as overlays, and copy/paste bridge.

### Running locally (manual)
```bash
WASH_DISPLAY=1 make wash        # build everything incl. out/wash-display
make run                        # router on :11000 (or: out/wash-router -no-auth -listen 0.0.0.0:11000 -apps-dir ./out)
```
Open `http://localhost:11000`, launch the Terminal app, and run the test guest
(above) — it inherits `DISPLAY`/`WAYLAND_DISPLAY` from the shell (DISPLAY_ENV.md).
The compositor's logs land in the router's stderr.

> **Aside — router spawn race (fixed here).** Running the *full* app set (not the
> minimal e2e config) crowds the first browser-connect background-app sweep and
> exposed a pre-existing router race: a fast child could dial back before its
> pid-keyed pending-attach slot was registered, fall through to the fresh-attach
> branch, and get killed by `spawnAndRun`'s 10s timeout. Fixed by holding
> `pendingMu` across `Spawn` + register (`internal/router/router.go`). Unrelated to
> the display feature, but it's why an X client could "die after ~10s" on a fresh
> session. See [[wash_router_spawn_race]].
