// Design tokens for @wash/ui. Dark theme by default; the color values
// resolve through CSS custom properties so a "pack" can re-skin the
// whole desktop live (see web/lib/src/packs.ts). Each color is
// `var(--wash-<name>, <hex>)`: the literal hex stays as the fallback,
// so anything rendering before a pack is applied — or with no pack at
// all — looks exactly as it did before. A pack sets the matching
// `--wash-*` variables on document.documentElement; because native
// apps render into light DOM (see define-app.tsx), those vars cascade
// into every open window and re-theme it with no re-render.
//
// Components reference these names rather than literal hex codes so
// there's still one place to change. Non-color tokens (spacing, radii,
// fonts, shadows, z-index, animation) stay literal — they're layout,
// not palette, and packs don't touch them.

export const tokens = {
  // Surfaces.
  bgWindow: 'var(--wash-bg-window, #181828)',
  bgMenu: 'var(--wash-bg-menu, #15152a)',
  bgInset: 'var(--wash-bg-inset, #10101a)', // sunken surface — inputs, log/code panes; darker than the window
  bgRow: 'var(--wash-bg-row, transparent)',
  bgRowHover: 'var(--wash-bg-row-hover, #202037)',
  bgRowSelected: 'var(--wash-bg-row-selected, #2a2a4a)',
  bgBackdrop: 'var(--wash-bg-backdrop, rgba(0,0,0,0.45))',
  // Drag-and-drop landing zone. A blue-tinted surface that reads
  // clearly different from the purple-grey row selection, so "this is
  // where the drop lands" is unmistakable during a drag. Pairs with
  // borderDropTarget (a solid accent-blue ring).
  bgDropTarget: 'var(--wash-bg-drop-target, #1e2b50)',

  // Borders.
  borderMenu: 'var(--wash-border-menu, #2a2a3a)',
  borderWindow: 'var(--wash-border-window, #2a2a3a)',
  borderFocus: 'var(--wash-border-focus, #3a3a6a)',
  borderDropTarget: 'var(--wash-border-drop-target, #6090e0)', // == accentBlue; the drop-zone ring

  // Foreground.
  fg: 'var(--wash-fg, #eee)',
  fgMuted: 'var(--wash-fg-muted, #888)',
  fgDim: 'var(--wash-fg-dim, #666)',

  // Danger / destructive accents (delete, replace).
  bgDanger: 'var(--wash-bg-danger, #7a1f1f)',
  borderDanger: 'var(--wash-border-danger, #a02d2d)',
  fgDanger: 'var(--wash-fg-danger, #fca5a5)',

  // Semantic status tones — bg/fg pairs for state badges and chips
  // (service active/failed, package install ok/fail, vscode warn…).
  // One vocabulary so every app's status pill reads the same.
  bgSuccess: 'var(--wash-bg-success, #1c3d24)',
  fgSuccess: 'var(--wash-fg-success, #86efac)',
  bgWarning: 'var(--wash-bg-warning, #3a3a1c)',
  fgWarning: 'var(--wash-fg-warning, #fde047)',
  bgInfo: 'var(--wash-bg-info, #1c2d3d)',
  fgInfo: 'var(--wash-fg-info, #93c5fd)',
  bgNeutral: 'var(--wash-bg-neutral, #1f1f2a)', // pairs with fgMuted for "inactive/static"

  // Permission-denied banner — distinct amber-brown so it reads as
  // "blocked, not broken" next to the red danger banner.
  bgDenied: 'var(--wash-bg-denied, #3a2a12)',
  borderDenied: 'var(--wash-border-denied, #7a5a20)',

  // Log/priority severities — text colors for log lines and level
  // strips, brighter than the status tones since they sit on rows.
  // See severityColor() for the syslog-priority → color mapping.
  sevError: 'var(--wash-sev-error, #ff7a7a)',
  sevWarn: 'var(--wash-sev-warn, #f0c050)',
  sevNotice: 'var(--wash-sev-notice, #c0d8ff)',
  sevInfo: 'var(--wash-sev-info, #bbb)',
  sevDebug: 'var(--wash-sev-debug, #666)',

  // Accent hues — soft pastels on dark, the same register as the
  // launcher's generated accentFor() hues so hand-picked and hashed
  // accents share one visual language. Used to tint icons/badges that
  // want a per-widget identity (e.g. the right-sidebar section icons).
  accentRed: 'var(--wash-accent-red, #e26060)',
  accentAmber: 'var(--wash-accent-amber, #e0b25f)',
  accentGreen: 'var(--wash-accent-green, #5fbf85)',
  accentCyan: 'var(--wash-accent-cyan, #5fb6c8)',
  accentBlue: 'var(--wash-accent-blue, #6090e0)',
  accentViolet: 'var(--wash-accent-violet, #9a90e0)',

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
