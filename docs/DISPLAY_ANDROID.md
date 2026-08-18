# Android surfaces over wash-display — feasibility note

Status: **theoretical assessment** (2026-08-17). No implementation planned yet.
Question answered: how hard is it to stream an Android app into a wash window
over the *existing* wash-display pipeline — e.g. from a Cuttlefish surface?

Related: [DISPLAY.md](DISPLAY.md), [DISPLAY_ENV.md](DISPLAY_ENV.md),
[DISPLAY_E2E.md](DISPLAY_E2E.md) (the pixel-assertion machinery built there is
exactly what would e2e an Android window later), [AGENT.md](AGENT.md) §0 (an
Android surface is the canonical opaque tier-3 surface).

---

## 1. Verdict

**Architecturally cheap, practically moderate.** wash-display does not care
what its clients are: anything that presents a Wayland surface gets captured,
damage-tracked, WebP-encoded, streamed to `<wash-app-display>`, and receives
seat input — unchanged. The whole question reduces to: *can the Android
runtime be made to present itself as a Wayland client?* Yes, at three effort
tiers. The compositor needs **zero conceptual change** for an MVP; the real
costs are launcher plumbing on the Android side, the missing touch lane, and
WebP throughput until M6 (VP9/WebRTC) lands.

---

## 2. The tiers

### Tier 0 — works today, zero code

The stock Android Emulator (goldfish) is an ordinary Qt desktop app. Launched
from a wash terminal it is just another Xwayland/Wayland guest, exactly like
the chromium/firefox probes in `display-probe.cap.ts`. Not Cuttlefish, but it
is the existence proof that "Android in a wash window" already works, and a
free way to exercise the pipeline against Android-shaped content (60fps
animation, soft keyboard, portrait aspect).

### Tier 1 — Cuttlefish via crosvm's Wayland display backend

Cuttlefish runs on crosvm, and upstream crosvm has a **Wayland display
backend**: each guest scanout becomes a host `wl_surface`, and host seat input
flows back into the guest as virtio-input events. Point that backend at
wash-display's `WAYLAND_DISPLAY` (the compositor already exports it via
`env.publish` / `WASH_WAYLAND_DISPLAY`) and the Android display is just
another toplevel — the compositor genuinely cannot tell.

The work is *plumbing, not architecture*:

- Cuttlefish's launcher (`launch_cvd` / `cvd`) constructs the crosvm command
  line itself and hardwires its own WebRTC streamer. The integration is
  patching/overriding that invocation: add the Wayland display, drop the
  streamer. Config surgery, not compositor surgery.
- Protocol gaps to expect: crosvm's backend may want `zwp_linux_dmabuf` /
  viewporter niceties wash-display does not fully serve today. With
  `--gpu_mode=guest_swiftshader` buffers are shm and boring (slow but
  guaranteed); with gfxstream they are dmabuf, which needs the vendored
  wlroots 0.17.4 read-back path (`WLROOTS_VENDORED=1` — stock Ubuntu's 0.17.1
  lacks it).
- Estimate: a one-to-two-week spike for a working demo, not months.

### Tier 1.5 — the cheaper sibling: Waydroid

Android in an LXC container whose hwcomposer **is a Wayland client natively**
— no VM display plumbing at all. Crucially, Waydroid has a multi-window mode
where **each Android app is its own Wayland toplevel**, which maps onto wash
windows one-to-one far more naturally than Cuttlefish's monolithic phone
display. If the goal is "an Android *app* in a wash window" rather than
"Cuttlefish specifically", this is the shortest path, and a throwaway
Waydroid-against-wash-display run doubles as a brutal stress test of the
compositor (subsurfaces, EGL, portrait resize, high frame rates).

### Tier 2 — per-app windows from Cuttlefish

One Cuttlefish display = one wash window showing the whole phone. Splitting
*individual apps* into individual wash windows requires Android-side
desktop-windowing / virtual-display-per-app work. That is the genuinely hard
version, and it is hard on the Android side, not the wash side. Not proposed.

---

## 3. The three real costs (any tier)

1. **Touch.** wash-display has no touch path — the FE synthesizes pointer
   events only (deliberately out of scope in DISPLAY_FEATURES_H). Android
   tolerates a mouse pointer for basic driving, but gestures and multitouch
   need a touch event lane FE → `wlr_seat` touch. Bounded, known work; a
   natural feature H-item if a tier ever ships.
2. **Throughput.** Android UI is 60fps full-screen animation at phone
   resolution; per-frame lossy WebP over WS delivers watchable-but-not-native
   frame rates (damage tracking only helps when the screen is still). This is
   the M6 (VP9/WebRTC) motivation arriving early. There is also an irony to
   price in: Cuttlefish's own streamer already does WebRTC — Tier 1 decodes
   to a surface and then re-encodes. "Same pipeline" buys one code path and
   one FE element at the cost of a double encode until M6.
3. **Aspect & DPI.** A portrait 1080×2400 surface in a desktop shell wants
   sensible default sizing and the HiDPI scale path to be honest — the
   DISPLAY_E2E P1 grounding specs are the prerequisite confidence here.

---

## 4. Suggested spike ladder (when picked up)

1. Tier 0 sanity: stock emulator in a wash window; note fps via `fpsFromLog`.
2. Waydroid session against `WASH_WAYLAND_DISPLAY` (Tier 1.5) — cheapest
   real-Android data point; file every protocol error the compositor logs.
3. Only then decide whether Cuttlefish-specific plumbing (Tier 1) earns its
   keep — Cuttlefish's value over Waydroid is fidelity (real AOSP images,
   radio/sensor emulation), not display integration.
