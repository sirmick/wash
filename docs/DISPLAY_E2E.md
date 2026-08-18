# wash-display — e2e hardening plan

Status: **plan of record** (2026-08-17). Near-term goal. This document turns the
display test suite from "13 specs that run on one dev box" into a CI-enforced,
pixel-asserting gate for the nastiest subsystem in the tree (2.5k lines of
wlroots C++ spanning Wayland, X11, popups, clipboard, HiDPI and input
injection).

Related: [DISPLAY.md](DISPLAY.md) §12 (the two-layer testing doctrine this
extends), [DISPLAY_ENV.md](DISPLAY_ENV.md) (the env-propagation contract several
specs sequence on), [TEST_FLAKES.md](TEST_FLAKES.md) (A7/A10 — stale binaries,
stale log matches), [CONTROL_BUS.md](CONTROL_BUS.md) (the long-term
driving-at-scale story; this plan stays within today's harness).

---

## 1. Where testing stands today

Three tiers exist:

| Tier | What | Where it runs |
|---|---|---|
| Contract | `e2e/tests/display.spec.ts` (7 specs) against the **fake** display in `apps/test` — window lifecycle, canned-frame decode, input batching, popup overlay, cursor, clipboard | CI, every PR |
| Real compositor | `display-cpp`, `display-term-xclock`, `display-input-smoke`, `display-guest` (GTK3, both backends), `display-qt-popover`, settings Display panel (3) | dev box only — skipped in CI via `displaySkipReason()` |
| Manual | `e2e/capture/display-probe.cap.ts` (13 probes, `canvasStats`, `fpsFromLog`), `tmp/smoke.sh`, `wash-display/tools/xscale.c` | by hand |

Three structural holes dominate:

1. **CI never compiles the compositor, let alone runs it.** Green CI says
   nothing about `compositor.cpp`. Every real-tier spec silently skips.
2. **Almost no pixel truth.** One 1×1 canned-pixel assertion in the whole gated
   suite. The real-tier specs assert router log lines — good for protocol,
   blind to a compositor that maps windows and streams blank or mis-cropped
   frames.
3. **No gate target, no freshness guard.** `displaySkipReason()` checks only
   that `out/wash-display` *exists*; a stale binary hard-FAILs 5 specs (it has,
   in CI 2026-06-26 — TEST_FLAKES A7). There is no `make display-*` analogue to
   `net-matrix` for a fast inner loop.

---

## 2. P0 — the real tier runs in CI (headless, software-rendered)

The load-bearing move. Everything already written multiplies in value once it
runs on every PR.

**Why it should work with no GPU:** capture is
`wlr_renderer_read_pixels` on a headless output. With `WLR_RENDERER=pixman`
that is pure CPU. The CI clients are all shm (xclock, the GTK3 guest, the
testcard of §3) — no dmabuf anywhere, so Ubuntu 24.04's wlroots 0.17.1 lacking
dmabuf read-back is irrelevant here.

### 2.1 Spike (do first, half a day)

Run `wash-display` under `WLR_RENDERER=pixman` with no DRM device and confirm:
compositor starts, Xwayland comes up, `xclock` maps, `frame seq=` lines carry a
sane dirty rect, and the browser canvas is non-blank (`canvasStats.nonBlankPct
> 50`). Note: `tmp/smoke.sh`'s "needs a GL/EGL device" comment predates trying
pixman — this spike confirms or kills the whole phase. Fallback if pixman is
broken on 0.17: CI builds with `WLROOTS_VENDORED=1` (0.17.4) — slower job, same
outcome.

### 2.2 Make targets

```
make display-test        # build compositor (+ TEST_APP=1 multicall) then run
                         # ONLY the display specs — the fast inner loop
make display-test-ci     # same, plus WASH_E2E_REQUIRE_DISPLAY=1 (skips → failures)
```

Display specs are selected by file list, no new tagging scheme:

```
pnpm exec playwright test \
  tests/display.spec.ts tests/display-cpp.spec.ts \
  tests/display-term-xclock.spec.ts tests/display-input-smoke.spec.ts \
  tests/display-guest.spec.ts tests/display-qt-popover.spec.ts \
  tests/settings.spec.ts --grep "Display"
```

### 2.3 CI job

New job `display-e2e` in `ci.yml`, `ubuntu-24.04`, alongside (not inside) the
existing `e2e` job so the base suite's runtime is untouched:

```yaml
- apt-get install -y libwlroots-dev wayland-protocols libxkbcommon-dev \
    libpixman-1-dev libwebp-dev libxcb-icccm4-dev libxcb-ewmh-dev \
    xwayland x11-apps python3-gi gir1.2-gtk-3.0
- make wash-display
- WLR_RENDERER=pixman make display-test-ci
```

Qt6 (`display-qt-popover`) is deliberately **not** installed in v1 — that spec
keeps its compile-or-skip behavior; add `qt6-base-dev` later if the job's
runtime budget allows. `WASH_E2E_REQUIRE_DISPLAY=1` is what makes this a gate:
inside this job a skip is a red build (the skip-in-beforeEach pattern stays for
every other context — see the display-skip-placement rule).

### 2.4 Freshness guard

`displaySkipReason()` (`e2e/fixtures/router.ts:75`) additionally compares
`mtime(out/wash-display)` against the newest mtime under `wash-display/src/`,
`wash-display/CMakeLists.txt` and `cpp-sdk/wash/`. Stale →
`"out/wash-display is older than its sources — rebuild (make wash-display)"`.
Under `WASH_E2E_REQUIRE_DISPLAY=1` that reason FAILs; locally it skips loudly.
This retires the A7 stale-binary hard-fail class for good.

### 2.5 Orphan accounting as an assertion

`stopRouter()` already sweeps `killProcsUnder(appsDir)`. Promote the sweep's
result into signal: after SIGTERM + grace, if the scan still finds live
processes (compositor, Xwayland, guests) the fixture **fails the test** with
the process list, instead of silently SIGKILLing. This turns the historical
orphan-accumulation failure mode (inotify exhaustion, confused
settings.spec) into a per-spec red with a name attached. Escape hatch:
`WASH_E2E_ALLOW_ORPHANS=1` while debugging.

### P0 acceptance

- `display-e2e` CI job green on a PR that touches `compositor.cpp`.
- A deliberately stale binary produces the freshness message, not 5 FAILs.
- `make display-test` completes locally in under ~90s.

---

## 3. P1 — deterministic testcard guest + pixel round-trip

The display analogue of `apps/test`: a scriptable client whose pixels we
*chose*, so specs can assert content, not just liveness.

### 3.1 `tools/display-testcard.c`

Plain `wl_shm` + xdg-shell toplevel (reuses the wayland-scanner glue pattern
from `tmp/toplevel_client.c`). **No toolkit deps** — compiles with
`libwayland-dev` alone, so it runs in the P0 CI job. Interface:

```
display-testcard --ctl <fifo> [--log <path>] [--size WxH] [--title T]
```

Draws four solid quadrants (default: red TL, green TR, blue BL, white BR).
Commands, one per line on the fifo, each ACKed on the log:

```
fill <tl|tr|bl|br|all> <rrggbb>   # redraw + damage only that quadrant
resize <w> <h>
title <text>
quit
```

Log lines (`TESTCARD: ...`) mirror the `GUEST:` convention from
`display-testguest.py`: every configure (w, h, scale) and every commit
(damage rect). The spec creates the fifo in its tmpdir, launches the testcard
through the wash terminal (same env-propagation preamble as
`display-term-xclock`), and writes commands directly from Node — the harness
and the guest share a host.

### 3.2 New spec: `e2e/tests/display-testcard.spec.ts`

Skip = `displaySkipReason()` only (no toolkit to probe). Tests, in order:

1. **Pixel round-trip.** Window maps → sample a 5×5 patch at each quadrant
   center via `getImageData` → mean per-channel Δ ≤ 24 from the expected color
   (lossy-WebP tolerance; solid regions compress nearly clean). Kills the
   "streams blank/garbage" blind spot end to end: client shm buffer →
   pixman composite → read_pixels → WebP → WS → `createImageBitmap` → canvas.
2. **Damage correctness.** `fill tr 000000` → next `frame seq=… dirty=` rect
   is within the TR quadrant (±2px slack), TR patch reads black, and the other
   three patches are byte-stable (poll two frames to dodge in-flight ones).
3. **Geometry crop.** Canvas logical size equals the testcard's configured
   size — locks the M5c `xdg_surface_get_geometry` crop (no CSD-margin drift).
4. **Resize round-trip.** `resize 480 360` → `window.geometry` in the router
   log → canvas CSS box tracks → quadrant centers *recomputed for the new
   size* still match.
5. **Input grounding.** Click the exact center of quadrant BR →
   `inject win=N button left down` → testcard logs the enter/motion coords;
   assert they land inside BR. (Pointer coords reach the guest via
   wl_pointer — testcard logs them.)
6. **HiDPI grounding (M5b/M8 lock).** Force FE scale mode to 2 (the
   localStorage seam `wash-app-display` already persists), reload → canvas
   backing store doubles while the CSS box does not
   (`el.width == 2 * cssWidth`) → repeat test 5: the click at *logical*
   coordinates still lands in BR. This is the regression class that has bitten
   twice (M8 input offset, M5c crop); it gets a permanent red-to-green here.

### P1 acceptance

- All six green in the P0 CI job under pixman.
- Reverting the M8 input-offset fix (locally, as a sanity check of the spec)
  turns test 5/6 red.

---

## 4. P2 — semantics matrix on real toolkits

Incremental specs on established patterns; each keeps its own dependency skip
(`pyGtkMissing()`, Qt compile-or-skip) and rides in CI where §2.3 installs the
dependency.

1. **Keymap** (`display-keymap.spec.ts`, GTK3 guest): send
   `app_msg{kind:'display.set_keymap', layout:'fr'}` to `com.wash.display` via
   the control socket → press physical `KeyQ` on the focused guest → the
   testguest input-echo label (and `GUEST:` log) shows `a`. First coverage of
   the whole `detectKeyboardLayout → apply_keymap → xkb` path.
2. **Popup grab lifecycle** (extend `display-guest.spec.ts`): menu open →
   hover *across* the parent window keeps the menu mapped (grab redirect with
   coord translation); press outside → client dismisses (`popup` teardown in
   log, overlay canvas detached). Add the GTK4 `PopoverMenu` leg —
   `tools/display-testguest4.py` already exists and is referenced by nothing.
3. **Focus hygiene** (`display-focus.spec.ts`): two X11 windows (xclock +
   xeyes), focus A, hover A, focus B → assert A receives pointer *leave*
   (stuck-hover clear via `notify_clear_focus`), keys land in B.
4. **Clipboard image leg** (extend `display-guest.spec.ts`): guest copies a
   PNG → `clipboard guest->wash mime=image/png`; wash-side `clipboard_get`
   round-trips the bytes (PNG magic check suffices).

---

## 5. P3 — robustness and lifecycle

Where "nasty" code actually bites. All real-tier, all in the P0 CI job.

1. **Compositor restart under load** (extend the settings restart spec):
   xclock open → Restart via panel → old window torn down cleanly
   (`window.destroy`, element detached), `env.publish` re-fires with fresh
   `WASH_X_DISPLAY`, a *new* terminal launches xclock successfully. NB the
   DISPLAY_ENV contract: already-running terminals keep the dead display's
   vars — the spec pins that too (typed `xclock` in the old terminal fails).
2. **Guest crash** (`display-crash.spec.ts`): `kill -9` the xclock pid →
   `window.destroy` in log → FE element removed → no orphan under appsDir
   (the §2.5 assertion covers the rest).
3. **Reconnect / force-frame** (`display-resync.spec.ts`): window streaming →
   `page.reload()` → router suppresses ring replay for video and sends
   `EvtWindowForceFrame` → compositor `reset_delta()` → canvas repaints
   non-blank within the expect budget. Ties `resync_video_test.go` (BE unit)
   to a full-stack assertion.
4. **Env-publish race pinned** (`display-env-race.spec.ts`): spawn a terminal
   *before* the compositor's first `env.publish` lands → document-by-assertion
   the current behavior (no `DISPLAY` in that shell). When the race is fixed,
   this spec is the red-to-green.

---

## 6. P4 — nightly canaries (not per-PR)

These belong in the capture config's cadence (`playwright.screenshots.config.ts`
style, 60s+ budgets), run by a scheduled CI job or by hand — never in the 25s
suite.

- **fps floor:** `fpsFromLog` on the `gst-video` probe ≥ threshold (start
  lenient, e.g. 15fps under pixman; ratchet later).
- **Damage efficiency:** `calc-dynamic` probe — mean dirty-rect area over 60
  frames < 20% of window area (a full-frame-every-time regression is the
  failure this catches).
- **Soak:** 50× open/close xclock cycles → compositor RSS and fd count bounded
  (±20% of baseline), zero surviving children.

---

## 7. Commit ladder

1. P0 spike result recorded here (pixman verdict + any renderer caveats).
2. `make display-test` / `display-test-ci` + freshness guard + orphan
   assertion (fixture-only change, no new specs).
3. `display-e2e` CI job, initially running only the existing 13.
4. `tools/display-testcard.c` + `display-testcard.spec.ts` (tests 1–4).
5. Testcard input grounding + HiDPI (tests 5–6).
6. P2 specs, one commit each (keymap first — zero current coverage).
7. P3 specs, one commit each (restart-under-load first).
8. P4 canaries + scheduled job.

Steps 1–3 are the near-term gate and are independent of everything after;
4–5 close the pixel blind spot; 6–8 are steady-state accretion.
