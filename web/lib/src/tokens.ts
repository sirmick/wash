// Design tokens for @wash/ui. Hardcoded dark theme today; a future
// theme switcher would swap this object behind a context provider.
// Components reference these names rather than literal hex codes so
// future-themers have one place to change.

export const tokens = {
  // Surfaces.
  bgWindow: '#181828',
  bgMenu: '#15152a',
  bgRow: 'transparent',
  bgRowHover: '#202037',
  bgRowSelected: '#2a2a4a',
  bgBackdrop: 'rgba(0,0,0,0.45)',

  // Borders.
  borderMenu: '#2a2a3a',
  borderWindow: '#2a2a3a',
  borderFocus: '#3a3a6a',

  // Foreground.
  fg: '#eee',
  fgMuted: '#888',
  fgDim: '#666',

  // Danger / destructive accents (delete, replace).
  bgDanger: '#7a1f1f',
  borderDanger: '#a02d2d',

  // Spacing.
  spaceXs: 4,
  spaceSm: 6,
  spaceMd: 8,
  spaceLg: 12,
  spaceXl: 16,
  spaceXxl: 18,

  // Border radius.
  radiusSm: 3,
  radiusMd: 4,
  radiusLg: 6,
  radiusXl: 8,

  // Font.
  fontSans: 'ui-sans-serif, system-ui, sans-serif',
  fontMono: 'ui-monospace, Menlo, Consolas, monospace',
  fontSizeSm: '11px',
  fontSizeMd: '12px',
  fontSizeBase: '13px',

  // Shadows.
  shadowMenu: '0 6px 16px rgba(0,0,0,0.5)',
  shadowModal: '0 12px 28px rgba(0,0,0,0.5)',
  shadowPalette: '0 16px 48px rgba(0,0,0,0.6)',

  // Z-index stack. Centralizing prevents the
  // "menu hides behind backdrop" surprise we hit twice already.
  zMenu: 1000,
  zDropMenu: 1950,
  zModal: 2000,
  zToast: 9000,
  zStartMenu: 10001,
  zPalette: 10002,

  // Animation. Names refer to @keyframes defined in shell/index.html
  // (must live in the document root since shadow-DOM-mounted apps
  // can't reach styles declared in their own bundles).
  animFadeIn: 'wash-fade-in 120ms ease-out',
  animPopIn: 'wash-pop-in 140ms ease-out',
  animPopInFast: 'wash-pop-in 100ms ease-out',
  animSlideUp: 'wash-slide-up 140ms ease-out',
} as const;

export type Tokens = typeof tokens;
