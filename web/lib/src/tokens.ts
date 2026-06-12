// Design tokens for @wash/ui. Hardcoded dark theme today; a future
// theme switcher would swap this object behind a context provider.
// Components reference these names rather than literal hex codes so
// future-themers have one place to change.

export const tokens = {
  // Surfaces.
  bgWindow: '#181828',
  bgMenu: '#15152a',
  bgInset: '#10101a', // sunken surface — inputs, log/code panes; darker than the window
  bgRow: 'transparent',
  bgRowHover: '#202037',
  bgRowSelected: '#2a2a4a',
  bgBackdrop: 'rgba(0,0,0,0.45)',
  // Drag-and-drop landing zone. A blue-tinted surface that reads
  // clearly different from the purple-grey row selection, so "this is
  // where the drop lands" is unmistakable during a drag. Pairs with
  // borderDropTarget (a solid accent-blue ring).
  bgDropTarget: '#1e2b50',

  // Borders.
  borderMenu: '#2a2a3a',
  borderWindow: '#2a2a3a',
  borderFocus: '#3a3a6a',
  borderDropTarget: '#6090e0', // == accentBlue; the drop-zone ring

  // Foreground.
  fg: '#eee',
  fgMuted: '#888',
  fgDim: '#666',

  // Danger / destructive accents (delete, replace).
  bgDanger: '#7a1f1f',
  borderDanger: '#a02d2d',
  fgDanger: '#fca5a5',

  // Semantic status tones — bg/fg pairs for state badges and chips
  // (service active/failed, package install ok/fail, vscode warn…).
  // One vocabulary so every app's status pill reads the same.
  bgSuccess: '#1c3d24',
  fgSuccess: '#86efac',
  bgWarning: '#3a3a1c',
  fgWarning: '#fde047',
  bgInfo: '#1c2d3d',
  fgInfo: '#93c5fd',
  bgNeutral: '#1f1f2a', // pairs with fgMuted for "inactive/static"

  // Permission-denied banner — distinct amber-brown so it reads as
  // "blocked, not broken" next to the red danger banner.
  bgDenied: '#3a2a12',
  borderDenied: '#7a5a20',

  // Log/priority severities — text colors for log lines and level
  // strips, brighter than the status tones since they sit on rows.
  // See severityColor() for the syslog-priority → color mapping.
  sevError: '#ff7a7a',
  sevWarn: '#f0c050',
  sevNotice: '#c0d8ff',
  sevInfo: '#bbb',
  sevDebug: '#666',

  // Accent hues — soft pastels on dark, the same register as the
  // launcher's generated accentFor() hues so hand-picked and hashed
  // accents share one visual language. Used to tint icons/badges that
  // want a per-widget identity (e.g. the right-sidebar section icons).
  accentRed: '#e26060',
  accentAmber: '#e0b25f',
  accentGreen: '#5fbf85',
  accentCyan: '#5fb6c8',
  accentBlue: '#6090e0',
  accentViolet: '#9a90e0',

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
