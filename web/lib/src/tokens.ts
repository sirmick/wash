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

// Font building blocks for the type scale below. Families and sizes are
// all var()-backed so a pack can re-skin type wholesale. `fontTitle`
// defaults to the sans family (nested var) unless a pack sets
// --wash-font-title (Copland → Chicago for titles only).
const FONT_SANS = 'var(--wash-font-sans, ui-sans-serif, system-ui, sans-serif)';
const FONT_MONO = 'var(--wash-font-mono, ui-monospace, Menlo, Consolas, monospace)';
const FONT_TITLE = `var(--wash-font-title, ${FONT_SANS})`;
// Three shared body/mono sizes (sm/md/lg) + two title sizes.
const SIZE_SM = 'var(--wash-text-sm, 11px)';
const SIZE_MD = 'var(--wash-text-md, 13px)';
const SIZE_LG = 'var(--wash-text-lg, 15px)';
const SIZE_TITLE_SM = 'var(--wash-title-sm, 13px)';
const SIZE_TITLE_LG = 'var(--wash-title-lg, 19px)';

export const tokens = {
  // Surfaces.
  bgWindow: 'var(--wash-bg-window, #181828)',
  bgMenu: 'var(--wash-bg-menu, #15152a)',
  bgInset: 'var(--wash-bg-inset, #10101a)', // sunken surface — inputs, log/code panes; darker than the window
  // Dead-black content canvas — the editor text surface and integrated
  // terminal containers. Distinct from bgInset because these want a true
  // black ("out-of-the-box Linux terminal") on dark packs, not the
  // slightly-tinted sunken grey. On dark packs this stays #000; LIGHT
  // packs override --wash-bg-canvas to their paper/white so the same
  // surface reads correctly there (Seoul = Solarized-light cream, NT =
  // white). Dark packs that omit it inherit the dead-black fallback.
  bgCanvas: 'var(--wash-bg-canvas, #000000)',
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
  accentOrange: 'var(--wash-accent-orange, #e0884f)',
  accentAmber: 'var(--wash-accent-amber, #e0b25f)',
  accentLime: 'var(--wash-accent-lime, #a8c45f)',
  accentGreen: 'var(--wash-accent-green, #5fbf85)',
  accentTeal: 'var(--wash-accent-teal, #5fc2a8)',
  accentCyan: 'var(--wash-accent-cyan, #5fb6c8)',
  accentBlue: 'var(--wash-accent-blue, #6090e0)',
  accentIndigo: 'var(--wash-accent-indigo, #7a82e0)',
  accentViolet: 'var(--wash-accent-violet, #9a90e0)',
  accentMagenta: 'var(--wash-accent-magenta, #c578d8)',
  accentPink: 'var(--wash-accent-pink, #e074a4)',

  // Spacing.
  spaceXs: 4,
  spaceSm: 6,
  spaceMd: 8,
  spaceLg: 12,
  spaceXl: 16,
  spaceXxl: 18,

  // Border radius. var()-backed (with px in the fallback) so a pack can
  // flatten corners — NT sets these to 0 for square 90s chrome. Call
  // sites use `${tokens.radiusMd}` WITHOUT a px suffix (px is baked in).
  radiusSm: 'var(--wash-radius-sm, 3px)',
  radiusMd: 'var(--wash-radius-md, 4px)',
  radiusLg: 'var(--wash-radius-lg, 6px)',
  radiusXl: 'var(--wash-radius-xl, 8px)',

  // Start-menu anchor: distance from the screen's bottom-left corner.
  // var-backed so a pack can float the menu off the edge + taskbar
  // instead of sitting near-flush (Copland nudges it out). `bottom` =
  // taskbar height (40px) + the gap above it; default is a 6px gap.
  startMenuLeft: 'var(--wash-startmenu-left, 6px)',
  startMenuBottom: 'var(--wash-startmenu-bottom, 46px)',

  // Font families. var()-backed so a pack can swap the family (e.g.
  // Copland's Chicago, Quicksand for Dreamtime). The comma-bearing
  // fallback is fine — everything after the var name's first comma is the
  // var() fallback. `fontTitle` is a SEPARATE family for headings/titles,
  // defaulting to the sans family unless a pack overrides --wash-font-title
  // (Copland points it at Chicago while body/mono stay on the Mac stacks).
  fontSans: FONT_SANS,
  fontMono: FONT_MONO,
  fontTitle: FONT_TITLE,
  // Back-compat raw sizes. Prefer the `type` scale below — these stay for
  // the handful of non-`font:`-shorthand call sites (icon sizing, etc.).
  fontSizeSm: '11px',
  fontSizeMd: '12px',
  fontSizeBase: '13px',

  // --- The type scale: the ONLY font styles the UI should use. ---
  // Eight `font:` shorthands so the whole desktop reads from one small set
  // — two titles + three body sizes + three mono sizes. Every family AND
  // size is var()-backed, so a pack re-skins type wholesale. Call sites use
  // `font: ${tokens.type.textMd}` (a complete weight/size/line-height/family
  // spec) instead of hand-assembling size + family.
  type: {
    titleLg: `700 ${SIZE_TITLE_LG}/1.25 ${FONT_TITLE}`,
    titleSm: `600 ${SIZE_TITLE_SM}/1.3 ${FONT_TITLE}`,
    textLg: `400 ${SIZE_LG}/1.45 ${FONT_SANS}`,
    textMd: `400 ${SIZE_MD}/1.45 ${FONT_SANS}`,
    textSm: `400 ${SIZE_SM}/1.4 ${FONT_SANS}`,
    monoLg: `400 ${SIZE_LG}/1.5 ${FONT_MONO}`,
    monoMd: `400 ${SIZE_MD}/1.5 ${FONT_MONO}`,
    monoSm: `400 ${SIZE_SM}/1.4 ${FONT_MONO}`,
  },

  // Desktop info-panel (banner) backdrop — a themeable darken + blur drawn
  // behind the top-left identity panel so it reads over a busy wallpaper.
  // Default OFF (transparent fill, no blur) so packs that rely on the
  // text-shadow halo are unchanged; a pack opts in by setting --wash-banner-bg
  // / --wash-banner-blur (Dreamtime darkens + blurs behind the panel).
  bannerBg: 'var(--wash-banner-bg, transparent)',
  bannerBlur: 'var(--wash-banner-blur, 0px)',

  // Per-theme border drawn around the wallpaper layer (which may be inset to
  // stop at the taskbar — see --wash-wallpaper-extent). A full CSS `border`
  // shorthand value; default off so packs that don't set it are unaffected.
  // Dreamtime uses `5px solid #000` for an elegant black frame.
  wallpaperBorder: 'var(--wash-wallpaper-border, 0 solid transparent)',

  // Shadows. shadowMenu is var()-backed so a pack can flatten menu/start-menu
  // drop shadows — Copland sets `--wash-shadow-menu: none` for an old-box look.
  shadowMenu: 'var(--wash-shadow-menu, 0 6px 16px rgba(0,0,0,0.5))',
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
  // The two menu open effects are var()-backed via a single
  // `--wash-menu-anim` so a pack can disable the open effect (set it to
  // `none`) — Copland does, for an instant old-box menu. Other packs keep
  // the distinct per-effect defaults below.
  animFadeIn: 'wash-fade-in 120ms ease-out',
  animPopIn: 'wash-pop-in 140ms ease-out',
  animPopInFast: 'var(--wash-menu-anim, wash-pop-in 100ms ease-out)',
  animSlideUp: 'var(--wash-menu-anim, wash-slide-up 140ms ease-out)',
  // Indeterminate progress. Mark the element `data-wash-spin` too, so the
  // reduced-motion rule in shell/index.html can still it without losing
  // the state it conveys.
  animSpin: 'wash-spin 900ms linear infinite',
} as const;

export type Tokens = typeof tokens;

// ---- accent resolution ----
// Icon colors (launcher rows, titlebar/taskbar) map onto the six themeable
// accent tokens so they re-skin with the pack, instead of baking a fixed
// hsl()/hex. The hues below are the approximate angles of the default
// accent hexes; a declared color snaps to the nearest, and an undeclared
// one is hashed onto the ring deterministically.
const ACCENT_RING: ReadonlyArray<{ token: string; hue: number }> = [
  { token: tokens.accentRed, hue: 0 },
  { token: tokens.accentOrange, hue: 25 },
  { token: tokens.accentAmber, hue: 45 },
  { token: tokens.accentLime, hue: 80 },
  { token: tokens.accentGreen, hue: 145 },
  { token: tokens.accentTeal, hue: 168 },
  { token: tokens.accentCyan, hue: 190 },
  { token: tokens.accentBlue, hue: 216 },
  { token: tokens.accentIndigo, hue: 242 },
  { token: tokens.accentViolet, hue: 268 },
  { token: tokens.accentMagenta, hue: 300 },
  { token: tokens.accentPink, hue: 330 },
];
const ACCENT_BY_NAME: Readonly<Record<string, string>> = {
  red: tokens.accentRed,
  orange: tokens.accentOrange,
  amber: tokens.accentAmber,
  lime: tokens.accentLime,
  green: tokens.accentGreen,
  teal: tokens.accentTeal,
  cyan: tokens.accentCyan,
  blue: tokens.accentBlue,
  indigo: tokens.accentIndigo,
  violet: tokens.accentViolet,
  magenta: tokens.accentMagenta,
  pink: tokens.accentPink,
};

// hueOfHex returns the HSL hue (0..359) of #rgb / #rrggbb, or null for a
// non-hex or a gray (no meaningful hue to snap).
function hueOfHex(color: string): number | null {
  const m = /^#?([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(color.trim());
  if (!m) return null;
  let hex = m[1];
  if (hex.length === 3) hex = hex[0] + hex[0] + hex[1] + hex[1] + hex[2] + hex[2];
  const r = parseInt(hex.slice(0, 2), 16) / 255;
  const g = parseInt(hex.slice(2, 4), 16) / 255;
  const b = parseInt(hex.slice(4, 6), 16) / 255;
  const max = Math.max(r, g, b);
  const d = max - Math.min(r, g, b);
  if (d === 0) return null; // gray
  let h: number;
  if (max === r) h = ((g - b) / d) % 6;
  else if (max === g) h = (b - r) / d + 2;
  else h = (r - g) / d + 4;
  return ((Math.round(h * 60) % 360) + 360) % 360;
}

function nearestAccent(hue: number): string {
  let best = ACCENT_RING[0];
  let bestDist = 360;
  for (const a of ACCENT_RING) {
    const raw = Math.abs(a.hue - hue);
    const dist = Math.min(raw, 360 - raw);
    if (dist < bestDist) {
      bestDist = dist;
      best = a;
    }
  }
  return best.token;
}

/**
 * accentColor resolves an icon's brand color to a pack-themeable accent
 * token, so it re-skins when the pack changes:
 *  - a hue name ("red"|"amber"|"green"|"cyan"|"blue"|"violet") → that token;
 *  - a hex color → the nearest accent hue's token;
 *  - any other declared color (e.g. a gray) → used verbatim;
 *  - nothing declared → `seed` hashed deterministically onto the ring, so
 *    every icon is colored and the same seed always lands on the same hue.
 */
export function accentColor(seed: string, declared?: string): string {
  if (declared) {
    const named = ACCENT_BY_NAME[declared.toLowerCase()];
    if (named) return named;
    const hue = hueOfHex(declared);
    if (hue !== null) return nearestAccent(hue);
    return declared;
  }
  let h = 0;
  for (let i = 0; i < seed.length; i++) h = ((h << 5) - h + seed.charCodeAt(i)) | 0;
  return ACCENT_RING[Math.abs(h) % ACCENT_RING.length].token;
}
