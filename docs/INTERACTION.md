# The interaction layer

How everything clickable in wash shows that it is clickable: hover, press,
keyboard focus, and disabled. One stylesheet, one attribute.

Source: `web/lib/src/hit.ts`. Guard: `scripts/check-interactive.py`
(`make check-interactive`, wired into `unit-test`).

## The problem it solves

Before this, the desktop had `:hover` styling in three files and `:active`
styling in **none**. Roughly 530 clickable sites — buttons, tabs, menu
items, titlebar controls, sidebar rows — looked identical whether you were
pointing at them, pressing them, or neither. The shared `<Button>`, at 114
call sites, had no hover, no press, no focus ring and no disabled
treatment.

The cause is structural, not neglect. wash chrome styles through inline
`style={{}}` objects, and **inline styles cannot express `:hover`,
`:active` or `:focus-visible`**. Nor can a plain stylesheet rescue them: an
inline `background` beats any selector, so a `.btn:hover { background: … }`
rule loses at every one of those sites. The seven places that did have
hover had each hand-rolled an `onMouseEnter` + `createSignal` pair.

## The mechanism

Tag any clickable element with `data-wash-hit`. The stylesheet draws the
state on an **`::after` overlay**.

A pseudo-element cannot be set by an inline style at all, so there is no
specificity fight and no rewrite of 530 style objects. It also means the
overlay needs no knowledge of what the element underneath is painted —
which matters, because 39 of these are `background: transparent` at rest
(menubar items, tabs, titlebar buttons, ghost buttons) and take their
appearance from whatever chrome is behind them.

```html
<button data-wash-hit onClick={…}>Save</button>
<div data-wash-hit="subtle" onClick={…}>Documents</div>
```

The layer also sets `cursor: pointer` — looking clickable is part of being
clickable — as a default, not `!important`, so an inline cursor still wins
where a call site means something more specific (`not-allowed` on a
disabled menu item, `move` on a titlebar).

Apps get the stylesheet from `defineWashApp`; the shell injects its own at
boot for its chrome (titlebars, resize handles, crash cards). Both paths
matter: apps mount into light DOM with no shadow root, so one stylesheet in
`document.head` reaches all of them.

### Intensities

| value | for |
|---|---|
| `data-wash-hit` | the default. Buttons, tabs, icon controls, chevrons. |
| `data-wash-hit="subtle"` | wide full-bleed targets where a 10% wash is heavy: list rows, file-tree rows, sidebar entries. |
| `data-wash-hit="strong"` | targets that want the emphatic solid highlight bar: menu rows, the menubar strip. |

`[disabled]` and `[aria-disabled="true"]` suppress the whole treatment, so
a disabled control reads as inert rather than merely dimmed.

## Why these two colours

Both are derived rather than declared, and were measured against the real
light and dark packs rather than guessed.

**Hover — `currentColor` at 10%.** This is the self-polarising part: the
text colour is near-white on the dark packs and near-black on Seoul/NT, so
one rule *lightens* dark chrome and *darkens* light chrome with no per-pack
table. A 10% tint can never clip.

**Press — black at 22% plus an inset well.** Darkening is the press
semantic on light and dark alike. On the dark packs this makes press flip
*direction* against hover, which is what makes it read as a press rather
than "more hovered"; on light packs it deepens past hover and the well
disambiguates.

Measured on the default dark pack: idle `rgb(21,21,42)` → hover
`rgb(42,42,61)` → press `rgb(11,11,22)`.

### The alternative that was rejected

`backdrop-filter: brightness(r)` — a literal ratio of whatever is behind
the overlay — is the obvious reading of "derive it from the background",
and it measures cleanly on dark: hover ×1.29, press ×0.81, identical for
transparent and solid elements. It was dropped because on the light packs
it **clips to solid white** (`#fdf6e3` × 1.30 → `#ffffff`), which loses the
tint entirely; press there is nearly indistinguishable from hover; and it
needs a per-pack polarity table. The tint is also cheaper and has no
stacking-context side effects.

## Timing

The effect arrives quicker than it leaves — responsive going on, unhurried
coming off. A transition declared on the base rule is what runs when a state
stops *matching*, so the base carries the fade-out and the state rules
override the fade-in:

| | duration | var |
|---|---|---|
| press in | 40ms | `--wash-hit-press-in` |
| hover in | 110ms | `--wash-hit-fade-in` |
| anything out | 220ms | `--wash-hit-fade-out` |

A press has to feel like it landed the instant the button went down;
anything slower reads as lag rather than as a fade.

**`background-color` must be in the transition list, not just `opacity`.**
Press and hover are different colours, so if only the opacity interpolates
the colour snaps at the state change while the opacity is still travelling
— and releasing a click flashes the hover white at close to press strength.
Measured before this was fixed: 16ms after mouseup the opacity was still
0.185 while the colour had already jumped from black to `#eee`, a ~100ms
white flash on every click. Interpolating both cross-fades press into hover
(`57 → 188 → 238` over the same window). `e2e/tests/interaction.spec.ts`
guards it.

For the same reason the overlay declares `background-color` rather than the
`background` shorthand: the longhand is what interpolates.

### Tuning

Every magnitude is var-backed, so a pack can retune or disable the whole
thing: `--wash-hit-hover`, `--wash-hit-press`, `--wash-hit-hover-opacity`
(`-subtle` / `-strong`), `--wash-hit-press-opacity` (likewise),
`--wash-hit-press-well`, and the three durations above.

## Keyboard focus

`:focus-visible` draws a 2px `--wash-border-focus` ring. It is `!important`
because 26 call sites set `outline: none` inline (mostly on inputs), and a
keyboard user losing the focus ring is not a style preference. It is
`:focus-visible` rather than `:focus`, so a mouse click never draws it.

## The guard

`make check-interactive` fails if a clickable element appears without the
layer, so the sweep cannot rot back to what it replaced. An element counts
as clickable if it is a `<button>`, declares `cursor:'pointer'` (inline or
via a style constant/factory in the same file), or carries an `onClick`.

Two escapes, both explicit:

- `data-wash-hit` — it is a hit target.
- `data-wash-no-hit` — it takes a click but is not a thing you point *at*:
  a dismiss backdrop, a scroll viewport that clears the selection on empty
  space. There are five. Marked rather than left bare so a decision is
  distinguishable from an oversight.

Component tags (`<Button>`, `<MenuItem>`, `<Tab>`) are skipped — they carry
the attribute internally. Replaced elements (`input`, `select`, `textarea`,
`img`) are skipped because they cannot render an `::after` overlay; a
`<select>` therefore has no hover treatment, which is a real gap and the
one place the layer does not reach.

### Two parser details, both found by the guard failing to fail

- It **ignores comments inside an opening tag**. `Button`'s comment
  mentions `data-wash-hit` while explaining attribute order, which would
  otherwise have satisfied the guard forever.
- It **anchors on real HTML element names** rather than scanning for
  `<Identifier`. TypeScript generics (`<Entry>`, `<string>`) are lexically
  identical to JSX tags and desynchronise a naive scanner — that is how the
  window close button escaped the first sweep, together with an apostrophe
  inside a `//` comment ("the titlebar's drag handler") reading as a string
  delimiter and swallowing the rest of the tag.

## Tests

`e2e/tests/interaction.spec.ts` asserts the **rendered** state via
`getComputedStyle(el, '::after')`, not the markup. Checking the attribute
alone would still pass if the stylesheet were never injected, which is the
actual regression to fear: either injection path going missing is silent.

One gotcha when writing more of these — `getComputedStyle` reports the
value a transition is *currently* at, so reading straight after `.hover()`
returns the idle `0` and looks exactly like a missing rule. Wait out the
90ms crossfade.

## Related: `<Tab>`

`web/lib/src/tab.tsx` is the document-tab control (wash-edit's files,
wash-term's shells), extracted in the same sweep because the two
implementations had drifted on height, radius, active colour, font and
close affordance. wash-net's underline section nav and the sidebar's icon
rail are deliberately *not* folded in: different controls that happen to
share the word.
