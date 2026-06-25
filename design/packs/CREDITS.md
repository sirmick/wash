# Pack wallpaper credits

Built-in pack wallpapers are derived from third-party artwork. Attributions
and licenses below (all compatible with wash's AGPL-3.0).

## Midnight — `wallpapers/midnight.svg`
Vectorized from **"04. Catppuccin Mocha"** by fr0st-xyz
(<https://github.com/fr0st-xyz/wallz>), GPL-3.0. Hibiscus + bird-of-paradise
motif isolated and traced with `vtracer`, composed on a `#232739` field.

## Tokyo — `wallpapers/tokyo.svg`
Vectorized from **"03. Solarized Dark"** by fr0st-xyz
(<https://github.com/fr0st-xyz/wallz>), GPL-3.0. Neon-alley scene traced with
`vtracer` (`--filter_speckle 6 --color_precision 5`). Source raster kept at
`design/packs/src/solarized-dark-source.png`.

## Oslo — `wallpapers/oslo.svg`
Vectorized from **"02. Nordic Blue"** by fr0st-xyz
(<https://github.com/fr0st-xyz/wallz>), GPL-3.0. Minimalist Nordic
mountain-twilight scene traced with `vtracer` (`--filter_speckle 6
--color_precision 6`). Paired with the Nord palette. Source raster kept at
`design/packs/src/nordic-blue-source.jpg`.

## Dreamtime — `wallpapers/dreamtime.svg`
Vectorized from an AI-generated Australian Aboriginal dot-painting — sun over
the water, green hinterland, fish + crocodile, a winding river. Traced with
`vtracer` at a clean setting (`--mode polygon --filter_speckle 4
--color_precision 6 --gradient_step 24`) so each dot is a single solid
polygon, ~16.3 k shapes — a higher-precision trace fragmented each dot into
several colour layers, which left polygon slivers showing between the
circles. A post-pass (`design/packs/circlify.py`) then turns it into a clean,
NON-OVERLAPPING dot field: (1) every round/square blob becomes a circle
candidate (radius clamped so a big merged region can't dominate); (2) a
greedy largest-first pass keeps a dot only if it does not overlap an
already-kept one — otherwise the dot is ABSORBED, folding its colour into
the keeper, whose final fill is the per-channel MEDIAN of everything it
absorbed (~16 k candidates → ~6 k mutually non-overlapping dots); (3)
over-long connector strokes are dropped. So no two circles overlap, the
colour of a coalesced dot is the median of the dots it merged, and the
croc/fish/river still read. Framed with a **black border on a black field**.
Minified with `svgo` (~0.3 MB). Source raster at
`design/packs/src/dreamtime-source.png`; converter at
`design/packs/circlify.py`. Paired with the dark Dreamtime palette (black
chrome, solid bright painting colors).

## Seoul — `wallpapers/seoul.svg`
Composed from three Korean **hwatu** (화투) 광/光 "light" cards — January
crane (송학), March cherry curtain (벚꽃), August moon (공산명월) — the 삼광
Samgwang hand. Card SVGs from **Wikimedia Commons**
(`Hwatu_January_gwang.svg`, `Hwatu_March_gwang.svg`, `Hwatu_August_gwang.svg`),
**CC BY-SA 4.0**. Inlined (ids/classes namespaced) and laid out side by side on
a cream field by `design/packs/compose-seoul.py`. Sources kept under
`design/packs/src/hwatu/`. Per CC BY-SA, the composed `seoul.svg` is likewise
available under CC BY-SA 4.0.

# Pack font credits

Packs that bundle a web font ship its woff2 (+ license/source note) under
`web/shell/public/fonts/`.

## Chicago — Copland's UI font
The scalable (TrueType) **Chicago** — Susan Kare's 1984 Macintosh system
typeface, vectorized by Bigelow & Holmes — converted to woff2 from
`Chicago v0.5.5.ttf` in <https://github.com/nikdog/chicago-font>. "Chicago"
is an Apple trademark/design; the upstream repo carries no explicit license,
so it is bundled on that basis at the maintainer's request. Provenance note
at `fonts/Chicago-SOURCE.txt`.

## Quicksand — Dreamtime's UI font (SIL OFL 1.1)
**Quicksand** by Andrew Paglinawan (OFL), a rounded geometric sans. Static
Regular/Bold instances generated from the upstream variable font
(`google/fonts`) via `fonttools varLib.instancer`. Bundled as
`fonts/Quicksand-Regular.woff2` + `fonts/Quicksand-Bold.woff2`; license at
`fonts/Quicksand-LICENSE.txt`.
