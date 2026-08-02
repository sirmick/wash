// Scrollbar treatment for wash chrome.
//
// Two problems, one stylesheet:
//
//  1. **Overlay scrollbars paint over content, and content paints back.**
//     Chromium (and Safari, and Firefox on macOS) default to overlay
//     scrollbars: zero layout width, drawn on top. In a terminal that is
//     actively harmful — xterm sizes `.xterm-screen` to the FULL viewport
//     width (measured: 108 cols × 7.22px = 780px = the whole box), and
//     `.xterm-screen` is a positioned LATER sibling of `.xterm-viewport`,
//     so it paints above the bar. The result is a scrollbar that appears
//     while scrolling and is then partly overpainted by the next row
//     repaint. Styling ::-webkit-scrollbar opts the element out of overlay
//     mode into a classic, space-taking bar; `scrollbar-gutter: stable`
//     does the same where the pseudo-elements aren't honoured. Once the bar
//     takes space, xterm's own scrollBarWidth measures it, FitAddon
//     subtracts it, and the last column can no longer sit underneath.
//  2. **Native bars ignore the pack.** Everything else in wash re-skins
//     with the theme; a default grey scrollbar down the side of a Copland
//     or Tokyo terminal is the one piece that doesn't. The colours here are
//     the same CSS custom properties every token resolves through, so a
//     live pack switch takes them with it.
//
// Injected once per document, into document.head — wash apps are light-DOM
// custom elements (defineWashApp does not attach a shadow root), so one
// stylesheet reaches every app.

const STYLE_ID = '__wash_scrollbars__';

/** Class for any scrollable region that should wear the wash scrollbar. */
export const WASH_SCROLL_CLASS = 'wash-scroll';

/**
 * Additional class for a VERTICALLY scrolling region whose content must
 * never be laid out under the bar — it reserves the gutter permanently.
 *
 * Deliberately NOT part of WASH_SCROLL_CLASS: `scrollbar-gutter` reserves
 * on the inline axis, so putting it on a horizontally-scrolling strip (the
 * terminal's own tab bar, say) just steals 10px of width for a vertical
 * scrollbar that will never appear. Measured, not theorised.
 */
export const WASH_SCROLL_GUTTER_CLASS = 'wash-scroll-gutter';

// Width in px. Wide enough to grab with a mouse, narrow enough that a
// terminal doesn't lose a column it didn't have to.
const BAR = 10;

const CSS = `
.${WASH_SCROLL_CLASS}, .xterm-viewport {
  /* Firefox: a real, space-taking bar in the pack's colours. */
  scrollbar-width: thin;
  scrollbar-color: var(--wash-border-menu, #3a3a4a) transparent;
}
.${WASH_SCROLL_GUTTER_CLASS}, .xterm-viewport {
  /* Reserve the gutter even where the platform would overlay, so content
     is never laid out underneath the bar. Vertical scrollers only. */
  scrollbar-gutter: stable;
}
.${WASH_SCROLL_CLASS}::-webkit-scrollbar, .xterm-viewport::-webkit-scrollbar {
  width: ${BAR}px;
  height: ${BAR}px;
}
.${WASH_SCROLL_CLASS}::-webkit-scrollbar-track, .xterm-viewport::-webkit-scrollbar-track {
  background: transparent;
}
.${WASH_SCROLL_CLASS}::-webkit-scrollbar-thumb, .xterm-viewport::-webkit-scrollbar-thumb {
  background: var(--wash-border-menu, #3a3a4a);
  border-radius: ${BAR / 2}px;
  /* Inset the thumb so it reads as a floating pill rather than a slab
     welded to the edge. */
  border: 2px solid transparent;
  background-clip: padding-box;
}
.${WASH_SCROLL_CLASS}::-webkit-scrollbar-thumb:hover, .xterm-viewport::-webkit-scrollbar-thumb:hover {
  background: var(--wash-fg-muted, #888);
  background-clip: padding-box;
}
.${WASH_SCROLL_CLASS}::-webkit-scrollbar-corner, .xterm-viewport::-webkit-scrollbar-corner {
  background: transparent;
}
`;

/**
 * ensureScrollbarStyles injects the stylesheet once. Safe to call from any
 * app's mount path; subsequent calls are a single getElementById.
 */
export function ensureScrollbarStyles(): void {
  if (typeof document === 'undefined') return;
  if (document.getElementById(STYLE_ID)) return;
  const el = document.createElement('style');
  el.id = STYLE_ID;
  el.textContent = CSS;
  document.head.appendChild(el);
}
