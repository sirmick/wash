// Interaction states for wash controls — the hover, press, focus and
// disabled treatments that inline styles cannot express.
//
// Why a stylesheet at all, when everything else in wash is inline style?
// Because `:hover` / `:active` / `:focus-visible` are selectors, not
// properties: there is no inline-style spelling of them. Before this
// file the desktop had exactly three hand-rolled hover rules and no
// press or focus state anywhere — a button looked identical whether you
// were pointing at it, holding it down, or had tabbed to it.
//
// The one trick that makes it compose with inline styles: this sheet
// never sets `background` from a literal, it sets it from
// `var(--wash-btn-bg)`, and derives the hover/press fills FROM that same
// custom property. So a caller that wants a bespoke fill sets the custom
// property inline —
//
//     <button class="wash-btn" style={{ '--wash-btn-bg': tokens.bgInfo }}>
//
// — and gets a correctly-derived hover and press for free, instead of
// setting `background` inline (which wins on specificity and would leave
// the control visually dead under the cursor).
//
// Injected once per document into document.head, exactly like
// scrollbars.ts: wash apps are light-DOM custom elements, so one
// stylesheet reaches every app in every window.

import { tokens, hoverFill, activeFill, borderHoverFill } from './tokens';

// There is deliberately no [aria-pressed] / [data-active] "on" variant in
// the sheet below. A toggle's selected fill would have to come from here,
// but almost every toggle already sets --wash-btn-bg inline to pick its
// resting look — and inline always wins over a stylesheet rule, so the
// variant would silently do nothing in exactly the places that wanted it.
// A toggle instead picks its own fill inline for both branches:
//
//     '--wash-btn-bg': selected ? tokens.bgRowSelected : 'transparent'
//
// and the hover/press derive off whichever branch is live. One mechanism
// rather than two that quietly disagree.

const STYLE_ID = '__wash_controls__';

/**
 * Class for any clickable control that should wear the standard wash
 * hover / press / focus treatment. Pair it with a `data-variant` (see
 * ButtonVariant) to pick the resting palette; the default variant
 * applies when `data-variant` is absent.
 *
 * The <Button> component sets both for you. Reach for the raw class only
 * where a control can't be a <Button> — an <a> styled as a button, or an
 * app-local element with its own layout.
 */
export const WASH_BTN_CLASS = 'wash-btn';

/**
 * Class for a clickable REGION rather than a control: a file row, a
 * process row, a collapsible section header, a launcher tile, a tab.
 *
 * Separate from WASH_BTN_CLASS because the two want opposite things.
 * A button is a small shape with its own border and padding, and lifts
 * subtly. A row is edge-to-edge with no chrome of its own, and takes a
 * full-width fill — a "button" treatment on a file list would draw 200
 * little outlines. So this class sets ONLY background and cursor: it
 * cannot shift the layout of anything it's added to, which is what makes
 * it safe to sprinkle across list rows that were tuned by hand.
 *
 * Set --wash-row-bg for the resting fill (including the selected state);
 * hover and press derive off it, so a selected row still responds under
 * the cursor instead of freezing at its highlight.
 */
export const WASH_ROW_CLASS = 'wash-row';

// Chrome that is only meaningful while the pointer is over its
// container — a row's delete affordance, a tab's close box. Fades in on
// container hover and on keyboard focus, so a control that is invisible
// to the mouse is still reachable by tab.
export const WASH_REVEAL_CLASS = 'wash-reveal';
export const WASH_REVEAL_HOST_CLASS = 'wash-reveal-host';

// Press travel. One pixel of give: enough that a click feels like it
// landed, small enough that a toolbar of icon buttons doesn't visibly
// reflow. Skipped under prefers-reduced-motion along with the fades.
const PRESS_TRAVEL = '1px';

// State transition. Short enough to feel immediate (a button must not
// lag the cursor) but long enough to avoid a hard flicker when sweeping
// across a row of controls.
const TRANSITION = '90ms ease-out';

const CSS = `
.${WASH_BTN_CLASS} {
  /* Resting palette. Variants below override these three; a caller may
     override any of them inline and the derived states follow along. */
  --wash-btn-bg: ${tokens.bgMenu};
  --wash-btn-fg: ${tokens.fg};
  --wash-btn-border: ${tokens.borderMenu};

  /* The derived states. These resolve against whatever --wash-btn-bg
     computes to ON THIS ELEMENT, so a variant rule or an inline override
     re-derives them automatically — see tokens.ts for why mixing toward
     the foreground is the direction-agnostic move. */
  --wash-btn-bg-hover: ${hoverFill('var(--wash-btn-bg)')};
  /* Text normally holds still through the states; the hook exists for
     the cases where the hover fill changes hue enough that it can't
     (the titlebar close button going red under a light-on-dark pack). */
  --wash-btn-fg-hover: var(--wash-btn-fg);
  --wash-btn-bg-active: ${activeFill('var(--wash-btn-bg)')};
  --wash-btn-border-hover: ${borderHoverFill('var(--wash-btn-border)')};

  background: var(--wash-btn-bg);
  color: var(--wash-btn-fg);
  /* Always a 1px border, transparent where the variant doesn't want one.
     Reserving the space unconditionally is what keeps a control from
     jumping 2px when a state colours its border in — and border-box
     keeps that reservation from growing anything that was given an
     explicit width or height (icon buttons, tab strips, table headers),
     which is most of the chrome this class landed on. */
  border: 1px solid var(--wash-btn-border);
  box-sizing: border-box;
  border-radius: ${tokens.radiusSm};
  font: ${tokens.type.textMd};
  cursor: pointer;
  transition: background-color ${TRANSITION}, border-color ${TRANSITION}, color ${TRANSITION};
}

/* :not(:disabled) throughout — a disabled control must not light up
   under the cursor, or it reads as clickable and isn't. */
.${WASH_BTN_CLASS}:hover:not(:disabled):not([aria-disabled="true"]) {
  background: var(--wash-btn-bg-hover);
  border-color: var(--wash-btn-border-hover);
  color: var(--wash-btn-fg-hover);
}
.${WASH_BTN_CLASS}:active:not(:disabled):not([aria-disabled="true"]) {
  background: var(--wash-btn-bg-active);
  border-color: var(--wash-btn-border-hover);
  color: var(--wash-btn-fg-hover);
  transform: translateY(${PRESS_TRAVEL});
}

/* :focus-visible, not :focus — a mouse click must not leave a ring
   behind, but a tab stop must be unmissable. */
.${WASH_BTN_CLASS}:focus-visible {
  outline: 2px solid ${tokens.focusRing};
  outline-offset: 1px;
}

.${WASH_BTN_CLASS}:disabled,
.${WASH_BTN_CLASS}[aria-disabled="true"] {
  opacity: 0.45;
  cursor: default;
}

/* --- Variants: each sets only its resting palette. --- */
.${WASH_BTN_CLASS}[data-variant="danger"] {
  --wash-btn-bg: ${tokens.bgDanger};
  --wash-btn-border: ${tokens.borderDanger};
}
.${WASH_BTN_CLASS}[data-variant="ghost"] {
  --wash-btn-bg: transparent;
  --wash-btn-border: ${tokens.borderMenu};
}
.${WASH_BTN_CLASS}[data-variant="icon"] {
  --wash-btn-bg: transparent;
  --wash-btn-border: transparent;
}

/* --- Clickable regions: rows, section headers, tiles, tabs. --- */
.${WASH_ROW_CLASS} {
  --wash-row-bg: transparent;
  --wash-row-bg-hover: ${hoverFill('var(--wash-row-bg)')};
  --wash-row-bg-active: ${activeFill('var(--wash-row-bg)')};

  background: var(--wash-row-bg);
  cursor: pointer;
  transition: background-color ${TRANSITION};
}
.${WASH_ROW_CLASS}:hover:not([aria-disabled="true"]) {
  background: var(--wash-row-bg-hover);
}
.${WASH_ROW_CLASS}:active:not([aria-disabled="true"]) {
  background: var(--wash-row-bg-active);
}
/* Inset ring, unlike a button's. A row runs edge to edge, so an outline
   drawn outside it would be clipped by the scroll container or overlap
   the row above. */
.${WASH_ROW_CLASS}:focus-visible {
  outline: 2px solid ${tokens.focusRing};
  outline-offset: -2px;
}

/* --- Hover-revealed chrome. --- */
.${WASH_REVEAL_CLASS} {
  opacity: 0;
  transition: opacity ${TRANSITION};
}
.${WASH_REVEAL_HOST_CLASS}:hover .${WASH_REVEAL_CLASS},
.${WASH_REVEAL_HOST_CLASS}:focus-within .${WASH_REVEAL_CLASS},
.${WASH_REVEAL_CLASS}:focus-visible {
  opacity: 1;
}

@media (prefers-reduced-motion: reduce) {
  /* Keep every state — they carry meaning — but arrive at them
     instantly and without the press travel. */
  .${WASH_BTN_CLASS},
  .${WASH_ROW_CLASS},
  .${WASH_REVEAL_CLASS} {
    transition: none;
  }
  .${WASH_BTN_CLASS}:active:not(:disabled):not([aria-disabled="true"]) {
    transform: none;
  }
}
`;

/**
 * ensureControlStyles injects the stylesheet once. Safe to call from any
 * app's mount path; subsequent calls are a single getElementById.
 * defineWashApp() calls it for every app, and the shell calls it at boot
 * for its own chrome (taskbar, window frames, start menu).
 */
export function ensureControlStyles(): void {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_ID)) return;
  const el = document.createElement('style');
  el.id = STYLE_ID;
  el.textContent = CSS;
  document.head.appendChild(el);
}
