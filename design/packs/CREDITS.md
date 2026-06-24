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
Vectorized from an AI-generated (Google Gemini) Australian Aboriginal
dot-painting of a sunrise over the coast — sun, ocean swirls, golden beach
with animal tracks, green hinterland. Traced with `vtracer`
(`--filter_speckle 8 --color_precision 5 --gradient_step 24 --mode polygon`)
then minified with `svgo` (~640 KB). Source raster kept at
`design/packs/src/dreamtime-source.png`. Paired with the Dreamtime palette
(strong painting colors on warm sand).

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

Packs that bundle a web font ship its woff2 + license under
`web/shell/public/fonts/`. Both below are **SIL Open Font License 1.1**.

## Sysfont C — Copland's UI font
**FA Sysfont C** by Alina Sava (FontsArena, 2021), an OFL revival of the
original **Chicago** bitmap typeface (Susan Kare, 1984, for Apple's System).
"Chicago" is an Apple trademark, hence the Sysfont name. Bundled as
`fonts/Sysfont-Regular.woff2`; license at `fonts/Sysfont-LICENSE.txt`.

## Quicksand — Dreamtime's UI font
**Quicksand** by Andrew Paglinawan (OFL), a rounded geometric sans. Static
Regular/Bold instances generated from the upstream variable font
(`google/fonts`) via `fonttools varLib.instancer`. Bundled as
`fonts/Quicksand-Regular.woff2` + `fonts/Quicksand-Bold.woff2`; license at
`fonts/Quicksand-LICENSE.txt`.
