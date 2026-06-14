# wash-display — native X/Wayland compositor

`wash-display` is wash's optional native compositor backend: a headless
wlroots compositor that turns each real X11/Wayland toplevel into a wash
window, capturing per-surface buffers and streaming them as WebP over the
wash wire. It is **C++/CMake**, built entirely separately from the
`CGO_ENABLED=0` Go core — the native wlroots/codec deps never touch the
static Go binaries. Design and internals: [`docs/DISPLAY.md`](../docs/DISPLAY.md).

Windows are **interactive**, not just streamed: pointer (move/click/scroll)
and keyboard with router-authoritative focus, bidirectional clipboard
(Wayland + X11), menus/dropdowns as positioned overlays (Wayland xdg-popup
and X11 override-redirect), and cursor-shape forwarding. See
[`docs/DISPLAY.md` §12](../docs/DISPLAY.md) for the milestone breakdown and
what's deferred (HiDPI, bitmap cursors, popup keyboard focus, WebRTC/audio).

It is **opt-in**: a normal `make` does not build it.

## Try it (manual)

`tools/display-testguest.py` is a tiny PyGObject **GTK3** guest (no build) that
exercises every interactive feature for manual debugging — a right-click
menu, copy/paste (`c`/`v`/`m` keys), and a status label echoing input. Run it
**from a wash terminal** (so it inherits the compositor's
`DISPLAY`/`WAYLAND_DISPLAY`):

```bash
GDK_BACKEND=wayland python3 tools/display-testguest.py   # xdg_popup menus
GDK_BACKEND=x11      python3 tools/display-testguest.py   # override-redirect menus (p4v's path)
```

The compositor shares the router's log, so the plumbing is observable there:
`inject … button`, `popup mapped` / `X11 popup mapped`, `clipboard guest->wash`,
`cursor … shape=`. The faked-BE contract e2e (`e2e/tests/display.spec.ts`)
runs in CI without a compositor; the real-client checks
(`display-input-smoke.spec.ts`, `display-guest.spec.ts`) are gated out of the
default `make e2e`.

## Build

### Dependencies (Debian / Ubuntu)

```bash
sudo apt install build-essential cmake pkg-config \
  libwlroots-dev libwayland-dev libwayland-bin wayland-protocols \
  libxkbcommon-dev libpixman-1-dev libwebp-dev \
  libxcb1-dev libxcb-icccm4-dev libxcb-ewmh-dev   # last 3: optional X11/Xwayland bridge
```

Fedora/RHEL equivalents: `wlroots-devel wayland-devel wayland-protocols-devel
libxkbcommon-devel pixman-devel libwebp-devel libxcb-devel xcb-util-wm-devel`
plus `cmake`/`gcc-c++`/`pkgconf`.

> **wlroots version caveat.** The build links **system** wlroots via
> `pkg-config` (it tries `wlroots-0.17`, then bare `wlroots`). Stock Ubuntu
> 24.04 ships 0.17.1, which lacks the GPU pixel read-back the capture
> pipeline wants; 0.17.4 has it. A pinned 0.17.4 source copy is vendored
> under `third_party/wlroots/` (see its `PROVENANCE.md`) for a future
> static build, but it is **not wired into CMake yet** — today you get
> whatever the distro ships. Full rationale: `docs/DISPLAY.md` §9a.

### Compile

Via the top-level Makefile (drops the binary in `out/`):

```bash
WASH_DISPLAY=1 make            # builds out/wash-display alongside the rest
```

Or standalone:

```bash
cmake -S wash-display -B wash-display/build -DCMAKE_BUILD_TYPE=Release
cmake --build wash-display/build               # → wash-display/build/wash-display
```

The build **auto-detects** what it can link:

- **Always** builds the wire client + WebP back-half (needs only `libwebp`).
  This is what proves the contract without a compositor.
- **When wlroots + wayland + xkbcommon + pixman are present**, it adds the
  real compositor (`WASH_DISPLAY_COMPOSITOR`).
- **When xcb + xcb-icccm are present**, it adds the Xwayland (X11) bridge
  (`WASH_DISPLAY_XWAYLAND`); `libxcb-ewmh-dev` is optional (a local shim in
  `src/xcbshim/` covers its absence).

Watch the `cmake` status lines — they report exactly which halves were
enabled (`wlroots … found — building compositor`, `xcb found — Xwayland
bridge enabled`, etc.).

## Test

There is no committed C++ unit suite and CI does **not** compile this
subsystem. Verification is two-tier:

- **Contract e2e (runs in CI, no compositor needed):** `e2e/tests/display.spec.ts`,
  `display-cpp.spec.ts`, `display-term-xclock.spec.ts` exercise the Go/FE side
  against a Go fake. `wash-display --wash-manifest` must stay valid JSON —
  `display-cpp.spec.ts` greps it.
- **Local smoke harness (manual):** a fake router + the headless compositor +
  a minimal `xdg_toplevel` client, documented in `docs/DISPLAY.md` §9a. It
  lives under the gitignored `tmp/` and needs a GL/EGL device, so it is not
  part of `make e2e`.

A quick manifest sanity check after building:

```bash
./out/wash-display --wash-manifest | python3 -m json.tool >/dev/null && echo OK
```

## Layout

```
src/                compositor.cpp, capture.cpp, wire_conn.cpp, encode.cpp,
                    encoders/ (WebP, copied from mac-phoenix), xcbshim/
third_party/        nlohmann/json.hpp (vendored), wlroots/ (vendored source,
                    not yet built — see PROVENANCE.md)
CMakeLists.txt      the build, with per-half auto-detection
```
