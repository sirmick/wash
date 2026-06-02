# Vendored wlroots — provenance

- **Upstream:** https://gitlab.freedesktop.org/wlroots/wlroots
- **Version:** 0.17.4 (see `meson.build` `version:`)
- **License:** MIT — see `LICENSE` in this directory.
- **Modifications:** none. This is an unmodified upstream checkout, vendored
  as source (not a fork — `git grep wash` over this tree is empty).

## Why it's here

wash-display targets wlroots **0.17.4** specifically: stock Ubuntu 24.04 ships
a frozen 0.17.1 that lacks the GPU pixel read-back the capture pipeline needs,
and 0.18 requires wayland ≥ 1.23 (the platform has 1.22). The rationale is in
[`docs/DISPLAY.md`](../../../docs/DISPLAY.md) §9a.

## Build status — IMPORTANT

This vendored tree is **not currently wired into the build**. As of today
`wash-display/CMakeLists.txt` links wlroots via `pkg-config` (system
`libwlroots-dev`, 0.17.x), and nothing references these sources. They are
staged for a future Meson sub-build (→ `libwlroots.a`) that pins 0.17.4
independent of the distro. Until that lands, treat this directory as reference
source, not a build input.

## Updating

Re-vendor by replacing the directory contents with a clean upstream checkout
at the desired tag and updating the **Version** line above. Keep it unmodified;
if a wash-local patch ever becomes necessary, record it here as a delta.
