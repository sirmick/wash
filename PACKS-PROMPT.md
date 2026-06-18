# wash-packs — handoff prompt

Pick this up by re-feeding this file. It captures the goal, the design we
agreed on, what's already proven, and the exact next steps. Branch:
`wash-packs` (worktree `branches/wash-packs`, off `main`).

## Goal

Add a few different **color schemes + desktop wallpapers**, bundled as
selectable **"packs"**. A pack re-skins the whole desktop coherently in one
click. Wallpapers must be **scalable** (vector) so they stay graceful at any
resolution — a modest centered motif on a calm color field, never stretched
(the spirit of today's flower-on-purple default). The taskbar **start icon**
is also re-skinnable per pack.

## The "pack" model

A pack = one bundle the user selects:

- **colorScheme** — the `--wash-*` CSS-variable chrome palette (surfaces,
  text, borders, accents).
- **wallpaper** — a scalable SVG: full-bleed field `<rect>` (color from the
  scheme) + a centered vector motif.
- **startIcon** — a pack-specific taskbar start-menu logo (SVG).
- *(later)* accent hue / taskbar tint.

`~/.config/wash/desktop.json` stores **one field** — `pack: "<id>"` — and
everything keys off it. The existing per-field overrides (custom wallpaper
`path`, `fallback_color`, clock, taskbar position) still win when set, so a
pack is a starting point, not a cage. Keep **"Midnight"** (today's chrome +
the flower) as the default pack so nothing regresses.

## Why this stays clean (architecture)

Native wash apps render into **light-DOM** custom elements (see
`web/lib/src/define-app.tsx`), NOT shadow DOM. So a CSS variable set on
`document.documentElement` cascades into every open app. That's the whole
trick for live theming:

1. In `web/lib/src/tokens.ts`, change the ~36 **color** values from literal
   hex to `var(--wash-…, <current hex>)`. Every one of the 36 call sites
   keeps working unchanged; the literal stays as the fallback default, so
   anything rendering before a pack is applied still looks right.
2. New `web/lib/src/packs.ts` — each pack = `{ id, name, scheme: {cssVar:
   value, …}, wallpaperSVG: string, startIconSVG: string }`. Export from
   `@wash/ui` (`web/lib/src/index.ts`). ONE source, read by both FEs below.
3. **Session FE** (`apps/session/fe/src/main.tsx`) applies the selected
   pack's vars to `document.documentElement` on the `desktop.config` msg (it
   already pokes `--wash-reserved-right` there — same hook). Re-themes every
   open app live, no re-render. It also swaps the wallpaper SVG + start icon
   (today the start button renders `washAssetUrl('wash-logo.svg')` around
   main.tsx:936 — make it render the pack's `startIconSVG` instead, falling
   back to the served logo).
4. **Settings FE** (`apps/settings/fe/src/main.tsx`) — replace/augment the
   raw wallpaper picker with a **pack gallery** (thumbnails + live preview),
   writing `pack` into `desktop.json`. Keep the existing "Choose image…"
   file picker for custom rasters.
5. **Session BE** (`apps/session/be/config.go`) — add a `Pack string` field
   to `desktopConfig` + `wallpaperPref`-level passthrough; ship it in the
   `desktop.config` app_msg. BE stays thin: **built-in pack wallpapers need
   NO BE round-trip** — the SVG is already in the FE bundle. The BE's
   read-bytes-from-disk path stays only for custom user images (`path`).

Decoupled-through-disk stays intact: settings writes `desktop.json`, session
fswatches it and re-ships `desktop.config`. (See [no premature service].)

## What's already proven (in `design/packs/`)

We confirmed the existing flower **vectorizes faithfully** — it's flat-color
illustration art (hibiscus + bird-of-paradise), not a photo, so it traces
back to clean SVG. Files:

- `flower-motif.svg` — the traced flower alone (459 paths, 235KB raw / 83KB
  gzipped), transparent field. **This is the nice new flower.**
- `flower-wallpaper.svg` — motif composed on the dark field `#232739`,
  16:9, `preserveAspectRatio="xMidYMid slice"`. Drop-in scalable wallpaper.
- `flower-wallpaper-preview.png` — rendered proof; matches the original at
  viewing distance.
- `flower-motif-isolated.png` — intermediate (field made transparent + tight
  crop) fed to the tracer.

Source raster: `apps/session/be/default-wallpaper.png` ("04. Catppuccin
Mocha" from github.com/fr0st-xyz/wallz).

### The vectorization pipeline (reusable for other packs' wallpapers)

Tools (installed this session): **vtracer** (`cargo install vtracer`, MIT,
at `~/.cargo/bin/vtracer`), **PIL** (`python3`/Pillow 10.2). No imagemagick.
SVG preview = the repo's Playwright (`@playwright/test`, run a render script
from inside `e2e/` so the module resolves; browsers in `~/.cache/ms-playwright`).

1. **Isolate motif** (PIL): read the PNG, sample bg from a corner, find the
   bbox of pixels differing from bg (threshold ~42), crop with ~20px pad,
   set near-bg pixels (threshold ~30) to transparent → `motif.png`.
2. **Trace** (clean/light): `vtracer --input motif.png --output motif.svg
   --colormode color --mode spline --filter_speckle 8 --color_precision 6`.
   For crisper hatch lines (heavier, ~1700 paths/150KB gz): `--filter_speckle
   2 --color_precision 8`.
3. **Compose** wallpaper SVG: `<svg viewBox="0 0 W H" preserveAspectRatio=
   "xMidYMid slice"><rect .. fill="FIELD"/><g transform="translate/scale">
   {motif inner}</g></svg>`. Field color comes from the pack scheme.

## Open decisions still to make

1. **The set** — ~4–5 dark packs, palette-named. Default = **Midnight**
   (current chrome + the flower). Then likely Catppuccin Mocha / Nord /
   Gruvbox / Tokyo Night. Light theme deferred unless asked.
2. **Other wallpapers** — either the user hands over a few more wallz images
   to run through the pipeline, OR we pick a handful. Each pack ideally pairs
   palette + a matching vectorized motif.
3. **Start-icon art** — source per-pack icons (Lucide is already a repo dep
   / Simple Icons CC0 for distro marks) or author originals.

## Next steps (build order)

1. `tokens.ts` → CSS-var indirection for the ~36 color tokens (keep hex
   fallbacks). Verify nothing visually changes (defaults == today).
2. `packs.ts` + export; define the **Midnight** default pack first using
   `design/packs/flower-wallpaper.svg`. Decide final asset home (likely
   inline strings in `packs.ts`, or `web/lib/src/packs/*.svg` imported as
   text by the bundler).
3. Session FE: apply scheme vars + wallpaper + start icon from the active
   pack on `desktop.config`.
4. Settings FE: pack gallery + write `pack` to `desktop.json`.
5. Session BE `config.go`: `Pack` passthrough in `desktop.config`.
6. Add remaining packs (palette + vectorized wallpaper + start icon).
7. Tests: keep `e2e/tests/settings.spec.ts` green; add coverage for
   pack-select → `desktop.json` write → live re-theme (full-stack e2e per
   [wash e2e pattern]).

## Standing conventions to honor

- Tiered green gate: build+unit green before commit; full all-test before
  push; ask local-main-ff vs remote-push before merging out of the worktree.
- Restart the live router (port 11000) after any change the user will test.
- Log convention `<component>: <verb> key=value: %v`.
- Tokens stay the single source for chrome/accents; per-domain local palettes
  stay local (see [wash UX tokens]).
