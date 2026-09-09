// The interaction layer — hover / press / focus feedback for everything
// clickable in wash. Full write-up: docs/INTERACTION.md.
//
// WHY A STYLESHEET AND NOT PROPS. wash chrome styles through inline
// `style={{}}` objects, and inline styles cannot express :hover / :active /
// :focus-visible. Before this file the desktop had hover in exactly three
// files and press feedback in none: ~530 clickable sites, and clicking them
// looked identical to not clicking them. Adding per-site JS hover signals
// (the seven that existed) does not scale and drifts.
//
// A plain stylesheet rule can't win either: `background` set inline beats any
// selector, so `.btn:hover { background: … }` loses at all 530 sites. But a
// PSEUDO-ELEMENT cannot be set inline at all, so an ::after overlay always
// wins with no specificity fight — and, crucially, it needs no knowledge of
// what the element's own background is. Tag an element `data-wash-hit` and it
// gets the whole treatment, whatever it's styled like.
//
// THE COLOURS ARE DERIVED, NOT DECLARED. Measured against the real packs
// (light and dark) rather than guessed:
//
//   hover — `currentColor` at ~10%. This is the self-polarising bit: the
//     text colour is near-white on the dark packs and near-black on Seoul /
//     NT, so one rule LIGHTENS dark chrome and DARKENS light chrome with no
//     per-pack table, and a tint at 10% opacity can never clip.
//   press — black at ~22% plus an inset well. Darkening is the press
//     semantic on light and dark alike, so on the dark packs press flips
//     direction against hover, which is what makes it read as a press
//     instead of "more hovered"; on light packs it deepens past hover and
//     the well disambiguates.
//
// The rejected alternative was `backdrop-filter: brightness(r)` — a literal
// ratio of whatever is behind. It measured cleanly on dark (hover ×1.29,
// press ×0.81, identical for transparent and solid elements) but clips to
// solid white on the light packs (#fdf6e3 × 1.30 → #ffffff), leaves press
// nearly indistinguishable from hover there, and needs a per-pack polarity
// table. The tint is cheaper and has no stacking-context side effects.
//
// Because the overlay reads `currentColor` and the pack's own CSS vars,
// everything here re-skins live with the pack like the rest of the tokens.
// Every magnitude is var-backed so a pack can retune or disable it.
//
// Injected once into document.head — wash apps are light-DOM custom elements
// (defineWashApp attaches no shadow root), so one stylesheet reaches every
// app, the shell chrome, and the settings panels alike.

const STYLE_ID = '__wash_hit__';

/**
 * The attribute that opts an element into hover/press/focus feedback.
 * Put it on ANY clickable thing — button, tab, menu item, list row,
 * titlebar control, sidebar entry:
 *
 *   <button data-wash-hit onClick={…}>Save</button>
 *   <div data-wash-hit="subtle" onClick={…}>Documents</div>
 *
 * Values tune the intensity only; the mechanism is identical.
 *   (empty)  — default. Buttons, tabs, menu items, icon controls.
 *   subtle   — wide, full-bleed targets where a 10% wash is heavy:
 *              list rows, file-tree rows, sidebar entries.
 *   strong   — small targets that need to shout: titlebar close,
 *              destructive confirmations.
 *
 * `[disabled]` and `[aria-disabled="true"]` suppress the whole treatment,
 * so a disabled control correctly reads as inert.
 */
export const HIT_ATTR = 'data-wash-hit';

const CSS = `
[${HIT_ATTR}] {
  /* Containing block for the overlay. Does not move the element; an
     inline position:absolute (some toolbar controls) still wins and is
     just as valid a containing block. */
  position: relative;
  /* Looking clickable is part of being clickable. An inline cursor still
     wins where a call site means something more specific (not-allowed on
     a disabled menu item, move on a titlebar), which is why this is a
     default rather than !important. */
  cursor: pointer;
}
[${HIT_ATTR}]::after {
  content: '';
  position: absolute;
  inset: 0;
  /* Matches whatever shape the element already is, including the square
     corners the NT pack sets via --wash-radius-*. */
  border-radius: inherit;
  pointer-events: none;
  background: var(--wash-hit-hover, currentColor);
  opacity: 0;
  transition: opacity 90ms ease-out, box-shadow 90ms ease-out;
}
/* Guarded: on touch, :hover latches after a tap and would leave the last
   thing touched looking permanently hovered. */
@media (hover: hover) {
  [${HIT_ATTR}]:hover::after { opacity: var(--wash-hit-hover-opacity, 0.10); }
  [${HIT_ATTR}="subtle"]:hover::after { opacity: var(--wash-hit-hover-opacity-subtle, 0.06); }
  [${HIT_ATTR}="strong"]:hover::after { opacity: var(--wash-hit-hover-opacity-strong, 0.16); }
}
[${HIT_ATTR}]:active::after {
  background: var(--wash-hit-press, #000);
  opacity: var(--wash-hit-press-opacity, 0.22);
  box-shadow: var(--wash-hit-press-well, inset 0 1px 3px rgba(0,0,0,0.35));
}
[${HIT_ATTR}="subtle"]:active::after {
  opacity: var(--wash-hit-press-opacity-subtle, 0.14);
}
[${HIT_ATTR}="strong"]:active::after {
  opacity: var(--wash-hit-press-opacity-strong, 0.30);
}
/* Keyboard focus. !important because 26 call sites set outline:none
   inline (mostly on inputs) and a keyboard user losing the focus ring is
   not a style preference. :focus-visible, so a mouse click never draws it. */
[${HIT_ATTR}]:focus-visible {
  outline: 2px solid var(--wash-border-focus, #3a3a6a) !important;
  outline-offset: 1px;
}
/* Disabled reads as inert: no hover, no press, no pointer. */
[${HIT_ATTR}][disabled],
[${HIT_ATTR}][aria-disabled="true"] {
  cursor: default;
  opacity: 0.5;
}
[${HIT_ATTR}][disabled]::after,
[${HIT_ATTR}][aria-disabled="true"]::after {
  display: none;
}
@media (prefers-reduced-motion: reduce) {
  /* The state still changes — only the crossfade goes. */
  [${HIT_ATTR}]::after { transition: none; }
}
`;

/**
 * ensureHitStyles injects the stylesheet once per document. Safe to call
 * from any mount path; subsequent calls are a single getElementById.
 * defineWashApp calls it for every app, and the shell calls it at boot for
 * its own chrome (titlebars, taskbar).
 */
export function ensureHitStyles(): void {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_ID)) return;
  const el = document.createElement('style');
  el.id = STYLE_ID;
  el.textContent = CSS;
  document.head.appendChild(el);
}
