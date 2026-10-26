# @wash/screensavers

The screensaver effects. Each one is built into a standalone ES module in the
shell assets (`screensavers/<id>.js`, with a `<id>.jpg` thumbnail), and the
shell loads it only when it starts. Idle detection, the overlay, the config
shape and the registry live in `web/lib/src/screensaver.ts`; the settings
are in the Desktop pane of Settings, saved to `desktop.json` as `screensaver`.

| id | Effect | Source |
|---|---|---|
| `logowriter3d` | 3D turtle graphics: 16 Logo programs, shuffled | `src/vendor/logowriter3d.js`, vendored from [sirmick/logowriter-gl-3d](https://github.com/sirmick/logowriter-gl-3d) |
| `harbor` | Night harbour city, reflected in the water | `src/harbor-night.ts` |
| `moire-interference` | Interference patterns from overlaid fine layers | `src/moire-interference.ts` |
| `moire-glow` | The classic bouncing-figure moiré, as glowing light | `src/moire-glow.ts` |

`blank` (a black screen) is built in and has no module.

## Adding one

1. Add `src/entries/<id>.ts` exporting `options` (its settings, as
   `SaverOption`s; Settings draws the controls from these) and
   `mount(container, opts) → { dispose }`. `mount` must fill `container`,
   stop drawing while `document.hidden`, and release everything (rAF, GL
   context, listeners, DOM) in `dispose`. Throw if the browser can't run it.
2. Add a 320×180 `thumbs/<id>.jpg`.
3. Add `{ id, name, module: true }` to `SCREENSAVERS` in
   `web/lib/src/screensaver.ts`.
4. Add the id to the loop in `e2e/tests/screensaver.spec.ts`.

`node build.mjs` (run by the shell build) writes the modules and thumbnails to
`web/shell/public/screensavers/`.

## Updating the vendored LogoWriter

Copy `logowriter3d.js` from the logowriter-gl-3d repo over
`src/vendor/logowriter3d.js` and update the commit in `src/vendor/README.md`.
If its `mount` options changed, update `src/vendor/logowriter3d.d.ts` too.
