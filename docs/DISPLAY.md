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
