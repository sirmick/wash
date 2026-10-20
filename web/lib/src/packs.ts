// Packs: one-click desktop themes. A pack bundles a color scheme (the
// --wash-* chrome palette), a wallpaper, and — optionally — a taskbar
// start icon. The user picks one (`pack` in desktop.json) and the whole
// desktop re-skins coherently.
//
// How the live re-theme works: native apps render into light DOM (see
// define-app.tsx), so CSS custom properties set on
// document.documentElement cascade into every open window. tokens.ts
// reads each color as `var(--wash-<name>, <hex>)` — the hex is a safety
// default used before any pack applies. applyScheme() sets a pack's
// vars (clearing any from a previously-active pack), so switching packs
// re-themes everything with no re-render.
//
// Wallpapers are NOT bundled. Each pack names an asset path (e.g.
// "wallpapers/midnight.svg"); the FE pulls the bytes via
// window.wash.fetchAsset (the router's asset.read channel — transport-
// agnostic, works in the in-browser VM). Built-in wallpapers ship
// embedded in the router; a user can drop their own into the asset
// drop spot (~/.config/wash/assets/wallpapers) and reference it from a
// pack — no rebuild. A custom wallpaper.path in desktop.json still
// overrides the pack wallpaper entirely.
//
// Only **Midnight** ships built in. The registry is an open array, so
// more packs — built-in or user-supplied — slot in by convention:
// `wallpapers/<id>.svg` + a scheme.

import { tokens } from './tokens';
import { washAssetUrl } from './assets';

export interface Pack {
  /** Stable id stored in desktop.json (`pack` field). */
  id: string;
  /** Human label for the settings gallery. */
  name: string;
  /**
   * Whether this is a light or dark theme. Chrome follows the scheme
   * vars regardless, but content apps with their own local palettes
   * (terminal xterm theme, editor syntax theme) read this to flip their
   * theme to match. Exposed to apps as the `--wash-appearance` var +
   * documentElement color-scheme by applyScheme().
   */
  appearance: 'light' | 'dark';
  /**
   * CSS custom properties applied to document.documentElement. Keys are
   * the `--wash-*` names from tokens.ts; values are this pack's colors.
   * Built-in packs define the full set so the chrome reskins coherently;
   * an omitted var falls back to the tokens.ts hex default.
   */
  scheme: Record<string, string>;
  /**
   * Wallpaper asset path (no leading slash), fetched via
   * window.wash.fetchAsset(). Convention: `wallpapers/<id>.svg`.
   */
  wallpaper: string;
  /**
   * Optional taskbar start-menu icon SVG markup. When omitted the FE
   * falls back to the served default logo (wash-logo.svg).
   */
  startIconSVG?: string;
  /**
   * Optional bundled web fonts this pack wants loaded. Each entry names
   * a CSS family (the same name the pack's `--wash-font-*` var
   * references) and the woff2 asset path(s). applyScheme registers them
   * via the FontFace API — the transport-agnostic path that also works
   * in the in-browser VM, where a plain CSS `url()` can't reach the
   * router's asset FS (same reason the terminal loads its fonts this
   * way). A pack that only references system-installed families omits
   * this. See web/shell/public/fonts/ for the bundled woff2.
   */
  fonts?: PackFont[];
}

/** A bundled web font a pack loads on apply. */
export interface PackFont {
  /** CSS family name — must match what the pack's --wash-font-* var uses. */
  family: string;
  /** Regular (400) woff2 asset path, no leading slash (e.g. `fonts/X.woff2`). */
  url: string;
  /** Optional bold (700) woff2 asset path. */
  boldUrl?: string;
}

// Module-level dedupe so a font loads once across pack switches /
// multiple applyScheme calls (the same shape the terminal uses for its
// own bundled fonts). Keyed by family name.
const loadedPackFonts = new Set<string>();

/**
 * Register a pack's bundled fonts via the FontFace API. Idempotent and
 * fire-and-forget: a failed load just leaves the family unregistered, so
 * the CSS font stack falls through to the next family. No-op under SSR /
 * where the Font Loading API is absent.
 */
function loadPackFonts(pack: Pack): void {
  if (!pack.fonts || typeof document === 'undefined' || !('fonts' in document)) return;
  for (const f of pack.fonts) {
    if (loadedPackFonts.has(f.family)) continue;
    loadedPackFonts.add(f.family);
    const faces = [new FontFace(f.family, `url(${washAssetUrl(f.url)})`, { weight: '400' })];
    if (f.boldUrl) faces.push(new FontFace(f.family, `url(${washAssetUrl(f.boldUrl)})`, { weight: '700' }));
    Promise.all(faces.map((face) => face.load().then((ld) => document.fonts.add(ld)))).catch(() => {
      loadedPackFonts.delete(f.family); // allow a later retry
    });
  }
}

// Midnight's palette IS the tokens.ts default: every color token is
// `var(--wash-<name>, <hex>)`, so we lift those (var-name, fallback-hex)
// pairs straight out of `tokens` into an explicit scheme. Midnight is
// therefore just another pack — not a privileged empty/fallback case —
// and there's one source of truth: edit a default in tokens.ts and
// Midnight follows automatically.
const midnightScheme = (): Record<string, string> => {
  const scheme: Record<string, string> = {};
  for (const value of Object.values(tokens)) {
    if (typeof value !== 'string') continue;
    const m = /^var\((--wash-[a-z-]+),\s*(.+)\)$/.exec(value);
    if (m) scheme[m[1]] = m[2];
  }
  return scheme;
};

// Midnight — today's chrome (lifted from tokens.ts) + the vectorized
// hibiscus/bird-of-paradise flower on a calm #232739 field. The default.
const midnight: Pack = {
  id: 'midnight',
  name: 'Midnight',
  appearance: 'dark',
  scheme: midnightScheme(),
  wallpaper: 'wallpapers/midnight.svg',
};

// Tokyo — Solarized Dark. A neon Kabukichō alley (vectorized from a
// flat-illustration wallpaper) on the canonical Solarized base03 field,
// with the full Solarized accent set for the chrome. First of the
// city-named packs.
const tokyo: Pack = {
  id: 'tokyo',
  name: 'Tokyo',
  appearance: 'dark',
  scheme: {
    '--wash-bg-window': '#002b36', // base03
    '--wash-bg-menu': '#00222c',
    '--wash-bg-inset': '#001a22',
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#073642', // base02
    '--wash-bg-row-selected': '#0d4a5a',
    '--wash-bg-backdrop': 'rgba(0,0,0,0.5)',
    '--wash-bg-drop-target': '#0a3d52',
    '--wash-border-menu': '#073642',
    '--wash-border-window': '#073642',
    '--wash-border-focus': '#1a6088',
    '--wash-border-drop-target': '#268bd2', // == accent blue
    '--wash-fg': '#93a1a1', // base1
    '--wash-fg-muted': '#657b83', // base00
    '--wash-fg-dim': '#586e75', // base01
    '--wash-bg-danger': '#3a161a',
    '--wash-border-danger': '#99342f',
    '--wash-fg-danger': '#e8746f',
    '--wash-bg-success': '#243010',
    '--wash-fg-success': '#9aab33',
    '--wash-bg-warning': '#33290a',
    '--wash-fg-warning': '#cba43a',
    '--wash-bg-info': '#06303d',
    '--wash-fg-info': '#3cb6ab',
    '--wash-bg-neutral': '#073642',
    '--wash-bg-denied': '#33200a',
    '--wash-border-denied': '#8a5418',
    '--wash-sev-error': '#e8746f',
    '--wash-sev-warn': '#d4a02a',
    '--wash-sev-notice': '#4ab3c0',
    '--wash-sev-info': '#839496', // base0
    '--wash-sev-debug': '#586e75',
    '--wash-accent-red': '#dc322f', // Solarized red
    '--wash-accent-orange': '#cb4b16', // Solarized orange
    '--wash-accent-amber': '#b58900', // yellow
    '--wash-accent-lime': '#8a9410',
    '--wash-accent-green': '#859900', // green
    '--wash-accent-teal': '#1f9e8a',
    '--wash-accent-cyan': '#2aa198', // cyan
    '--wash-accent-blue': '#268bd2', // blue
    '--wash-accent-indigo': '#4a63c4',
    '--wash-accent-violet': '#6c71c4', // violet
    '--wash-accent-magenta': '#d33682', // Solarized magenta
    '--wash-accent-pink': '#db5a96',
    // Darken + blur backdrop behind the desktop info panel so it reads over
    // the busy neon wallpaper.
    '--wash-banner-bg': 'rgba(0,15,20,0.42)',
    '--wash-banner-blur': '8px',
  },
  wallpaper: 'wallpapers/tokyo.svg',
};

// Seoul — Hwatu (Go-Stop). The first LIGHT pack: warm ivory paper
// surfaces + sumi-ink text, accents drawn from the cards and the Korean
// obangsaek palette. Wallpaper is the 삼광 (Samgwang) "three lights"
// hand — January crane, March cherry curtain, August moon (each bears
// the 光 "light" mark) — composed side by side on a cream field from
// hwatu card SVGs (Wikimedia Commons, CC BY-SA 4.0; see CREDITS).
const seoul: Pack = {
  id: 'seoul',
  name: 'Seoul',
  appearance: 'light',
  scheme: {
    '--wash-bg-window': '#f6efdd', // ivory paper
    '--wash-bg-menu': '#f0e7cf',
    '--wash-bg-inset': '#e9dec2', // sunken (slightly deeper on light)
    '--wash-bg-canvas': '#fdf6e3', // editor/terminal canvas — Solarized-light base3 (pairs with the light syntax theme)
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#e7dabc',
    '--wash-bg-row-selected': '#ecd6a6',
    '--wash-bg-backdrop': 'rgba(40,28,8,0.35)',
    '--wash-bg-drop-target': '#d4e3ec',
    '--wash-border-menu': '#d8c9a4',
    '--wash-border-window': '#cdbb90',
    '--wash-border-focus': '#2f6ea5', // obangsaek blue
    '--wash-border-drop-target': '#2f6ea5', // == accent blue
    '--wash-fg': '#2b2118', // sumi ink
    '--wash-fg-muted': '#6b5e4a',
    '--wash-fg-dim': '#978a72',
    '--wash-bg-danger': '#f7d9d5',
    '--wash-border-danger': '#d98a82',
    '--wash-fg-danger': '#b3201a',
    '--wash-bg-success': '#dbe8cf',
    '--wash-fg-success': '#3f7a2e',
    '--wash-bg-warning': '#f3e6c2',
    '--wash-fg-warning': '#9a6f15',
    '--wash-bg-info': '#d6e4ef',
    '--wash-fg-info': '#235f8a',
    '--wash-bg-neutral': '#e6dcc2',
    '--wash-bg-denied': '#f0e2c0',
    '--wash-border-denied': '#c99a4a',
    '--wash-sev-error': '#c0271f',
    '--wash-sev-warn': '#9a6f15',
    '--wash-sev-notice': '#235f8a',
    '--wash-sev-info': '#4a4030',
    '--wash-sev-debug': '#8a7d64',
    '--wash-accent-red': '#cc3433', // obangsaek/hwatu red
    '--wash-accent-orange': '#cf6a2e',
    '--wash-accent-amber': '#cf9b2e', // gold (card tassels)
    '--wash-accent-lime': '#7d9a35',
    '--wash-accent-green': '#4f8a3f', // dancheong green
    '--wash-accent-teal': '#2a9a6e',
    '--wash-accent-cyan': '#2a9a92',
    '--wash-accent-blue': '#2f6ea5', // obangsaek blue
    '--wash-accent-indigo': '#4a5aa8',
    '--wash-accent-violet': '#8a5fb0',
    '--wash-accent-magenta': '#a83f8c',
    '--wash-accent-pink': '#c2517e',
  },
  wallpaper: 'wallpapers/seoul.svg',
};

// Copland — Mac OS 8.5/9 "Platinum" appearance (named for Apple's Copland,
// the next-gen Mac OS whose Platinum look shipped in 8.5/9). Five shades of
// platinum grey, white sunken fields, black text, a light-grey pinstripe
// titlebar (NOT a colored caption), a raised 3D bevel, and a periwinkle-blue
// selection highlight — all sampled straight off a Mac OS 9 Appearance
// control-panel screenshot. Replaced the old Windows-NT pack. The wallpaper
// is a vectorized 1997 vintage-sunset design on black, so the platinum
// windows pop. A light theme.
const copland: Pack = {
  id: 'copland',
  name: 'Copland',
  appearance: 'light',
  scheme: {
    '--wash-bg-window': '#e0e0e0', // platinum (the dominant grey in the screenshot)
    '--wash-bg-menu': '#dddddd', // tabs / menu surfaces
    '--wash-bg-inset': '#ffffff', // white sunken fields
    '--wash-bg-canvas': '#ffffff', // editor/terminal canvas — Mac white
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#d0d0d0',
    '--wash-bg-row-selected': '#9aa0d6', // periwinkle-blue highlight (black text still reads)
    '--wash-bg-backdrop': 'rgba(0,0,0,0.3)',
    '--wash-bg-drop-target': '#c0c0f0', // light periwinkle (the screenshot's selection fill)
    '--wash-border-menu': '#9a9a9a',
    '--wash-border-window': '#555555',
    '--wash-border-focus': '#606090', // dark periwinkle (active/focus)
    '--wash-border-drop-target': '#606090',
    '--wash-fg': '#000000',
    '--wash-fg-muted': '#555555',
    '--wash-fg-dim': '#808080',
    '--wash-bg-danger': '#f0d4d4',
    '--wash-border-danger': '#a85050',
    '--wash-fg-danger': '#a00000',
    '--wash-bg-success': '#d4e6cc',
    '--wash-fg-success': '#2e6e2e',
    '--wash-bg-warning': '#efe6c2',
    '--wash-fg-warning': '#7a5e10',
    '--wash-bg-info': '#9aa0d6', // periwinkle info badge…
    '--wash-fg-info': '#1a1a40', // …with deep-blue text (Platinum highlight look)
    '--wash-bg-neutral': '#d0d0d0',
    '--wash-bg-denied': '#ece0c4',
    '--wash-border-denied': '#9a7a30',
    '--wash-sev-error': '#b00000',
    '--wash-sev-warn': '#7a5e10',
    '--wash-sev-notice': '#4a4a8a',
    '--wash-sev-info': '#444444',
    '--wash-sev-debug': '#808080',
    '--wash-accent-red': '#cc3b33',
    '--wash-accent-orange': '#dd7a2e',
    '--wash-accent-amber': '#caa23a',
    '--wash-accent-lime': '#7faa3c',
    '--wash-accent-green': '#3f9a52',
    '--wash-accent-teal': '#2f9a8c',
    '--wash-accent-cyan': '#2f95ac',
    '--wash-accent-blue': '#6c72b8', // Platinum periwinkle
    '--wash-accent-indigo': '#5a60a8',
    '--wash-accent-violet': '#8a5fb0',
    '--wash-accent-magenta': '#b23f95',
    '--wash-accent-pink': '#cc5b86',
    // Chrome override vars (only this pack sets them; window.tsx defaults
    // everywhere else): a light platinum pinstripe caption with black,
    // centered title text — the Mac OS 9 look, not a colored caption.
    '--wash-titlebar-active': '#cccccc',
    '--wash-titlebar-inactive': '#dddddd',
    '--wash-titlebar-fg': '#000000',
    // Raised Platinum bevel: bright highlight top-left, mid-grey bottom-right.
    '--wash-border-light': '#ffffff',
    '--wash-border-dark': '#909090',
    // Square corners everywhere — the crisp Platinum/9x look.
    '--wash-radius-sm': '0',
    '--wash-radius-md': '0',
    '--wash-radius-lg': '0',
    '--wash-radius-xl': '0',
    '--wash-taskbar-bg': '#cccccc',
    // Desktop info banner sits over the black 1997 wallpaper → light text.
    '--wash-banner-fg': '#e8e8f0',
    // Classic Mac type. Chicago — the original Macintosh System UI face
    // (Susan Kare, 1984; bundled scalable TrueType, see `fonts` below +
    // web/shell/public/fonts/Chicago-SOURCE.txt) — is used for TITLES only
    // (the two `type.title*` styles, via --wash-font-title), which is how
    // the classic Mac used it: window/menu titles in Chicago, body in
    // Geneva. Body + mono stay on the installed-Mac stacks.
    '--wash-font-title': 'Chicago, Geneva, "Lucida Grande", Verdana, Helvetica, sans-serif',
    '--wash-font-sans': 'Geneva, "Lucida Grande", Verdana, Helvetica, sans-serif',
    '--wash-font-mono': 'Monaco, "Courier New", monospace',
    '--wash-taskbar-top': '#ffffff',
    // Start menu sits flush in the bottom-left corner, rising straight off
    // the taskbar (40px) — the classic Win9x/Platinum look, no float gap.
    '--wash-startmenu-left': '0',
    '--wash-startmenu-bottom': '40px',
    // Old-box menus: appear instantly with no drop shadow, the way a 90s
    // machine drew them — no slide/pop animation, no float.
    '--wash-menu-anim': 'none',
    '--wash-shadow-menu': 'none',
    // Start menu in the same Platinum grey as the taskbar (#cccccc) rather
    // than the lighter menu tone, so the chrome reads as one piece.
    '--wash-startmenu-bg': '#cccccc',
    // Wallpaper stops at the taskbar (window-exclusive-of-taskbar), like
    // Dreamtime — the vintage art sits in the desktop area, not behind the bar.
    '--wash-wallpaper-extent': 'desktop',
  },
  wallpaper: 'wallpapers/copland.svg',
  fonts: [{ family: 'Chicago', url: 'fonts/Chicago.woff2' }],
};

// Oslo — the Nord palette (arctic blue-grey) + a vectorized Nordic
// mountain-twilight wallpaper (from wallz "02. Nordic Blue"). Dark theme.
const oslo: Pack = {
  id: 'oslo',
  name: 'Oslo',
  appearance: 'dark',
  scheme: {
    '--wash-bg-window': '#2e3440', // nord0 polar night
    '--wash-bg-menu': '#2a2f3a',
    '--wash-bg-inset': '#272c36',
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#3b4252', // nord1
    '--wash-bg-row-selected': '#434c5e', // nord2
    '--wash-bg-backdrop': 'rgba(0,0,0,0.5)',
    '--wash-bg-drop-target': '#34495e',
    '--wash-border-menu': '#3b4252',
    '--wash-border-window': '#3b4252',
    '--wash-border-focus': '#5e81ac', // nord10 frost
    '--wash-border-drop-target': '#88c0d0', // nord8
    '--wash-fg': '#e5e9f0', // nord5 snow storm
    '--wash-fg-muted': '#8893a5',
    '--wash-fg-dim': '#6b7488',
    '--wash-bg-danger': '#4a2c30',
    '--wash-border-danger': '#bf616a',
    '--wash-fg-danger': '#d3868d',
    '--wash-bg-success': '#2f3a2c',
    '--wash-fg-success': '#a3be8c', // nord14
    '--wash-bg-warning': '#3a3528',
    '--wash-fg-warning': '#ebcb8b', // nord13
    '--wash-bg-info': '#2a3744',
    '--wash-fg-info': '#88c0d0', // nord8
    '--wash-bg-neutral': '#3b4252',
    '--wash-bg-denied': '#3a3024',
    '--wash-border-denied': '#a07a4a',
    '--wash-sev-error': '#bf616a',
    '--wash-sev-warn': '#ebcb8b',
    '--wash-sev-notice': '#88c0d0',
    '--wash-sev-info': '#d8dee9', // nord4
    '--wash-sev-debug': '#6b7488',
    '--wash-accent-red': '#bf616a', // nord11 aurora
    '--wash-accent-orange': '#d08770', // nord12
    '--wash-accent-amber': '#ebcb8b', // nord13
    '--wash-accent-lime': '#c0cf8e',
    '--wash-accent-green': '#a3be8c', // nord14
    '--wash-accent-teal': '#8fc2b0',
    '--wash-accent-cyan': '#8fbcbb', // nord7 frost
    '--wash-accent-blue': '#81a1c1', // nord9 frost
    '--wash-accent-indigo': '#5e81ac', // nord10 frost
    '--wash-accent-violet': '#b48ead', // nord15
    '--wash-accent-magenta': '#c490b5',
    '--wash-accent-pink': '#d3a0b8',
    // Darken + blur backdrop behind the desktop info panel so it reads over
    // the wallpaper.
    '--wash-banner-bg': 'rgba(20,26,38,0.45)',
    '--wash-banner-blur': '8px',
  },
  wallpaper: 'wallpapers/oslo.svg',
};

// Dreamtime — an Australian Aboriginal dot-painting pack. A DARK theme:
// black chrome carrying solid, bright colors lifted from the painting
// (sun-orange, bright amber, emerald, turquoise, electric blue). Near-white
// text. The wallpaper is the dot-painting traced to real SVG circles, framed
// by a black border on a black field — so it floats on the black desktop.
// Bundles rounded-geometric **Quicksand** (OFL) for the chrome, echoing the
// painting's dots.
const dreamtime: Pack = {
  id: 'dreamtime',
  name: 'Dreamtime',
  appearance: 'dark',
  scheme: {
    // Black chrome — the wallpaper + bright accents carry all the color.
    '--wash-bg-window': '#0c0c0f',
    '--wash-bg-menu': '#141418', // menus / dropdowns / start menu
    '--wash-bg-inset': '#050506', // sunken field (darker)
    '--wash-bg-canvas': '#000000', // editor/terminal — true black
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#23232c',
    '--wash-bg-row-selected': '#e8631a', // solid bright sun-orange selection (light text reads)
    '--wash-bg-backdrop': 'rgba(0,0,0,0.6)',
    '--wash-bg-drop-target': '#0d3a44',
    '--wash-border-menu': '#26262e',
    '--wash-border-window': '#000000',
    '--wash-border-focus': '#ff6a00', // bright sun-orange focus ring
    '--wash-border-drop-target': '#1bc4e0',
    '--wash-fg': '#f1f1f5', // near-white
    '--wash-fg-muted': '#a2a2ae',
    '--wash-fg-dim': '#6a6a76',
    '--wash-bg-danger': '#3a1110',
    '--wash-border-danger': '#e62b1e',
    '--wash-fg-danger': '#ff6f60',
    '--wash-bg-success': '#0f3012',
    '--wash-fg-success': '#3ed94f',
    '--wash-bg-warning': '#33260a',
    '--wash-fg-warning': '#ffc23a',
    '--wash-bg-info': '#06303a',
    '--wash-fg-info': '#2ad0ec',
    '--wash-bg-neutral': '#1c1c24',
    '--wash-bg-denied': '#33240a',
    '--wash-border-denied': '#b0791c',
    '--wash-sev-error': '#ff5e4e',
    '--wash-sev-warn': '#ffb020',
    '--wash-sev-notice': '#2ad0ec',
    '--wash-sev-info': '#c4c4d0',
    '--wash-sev-debug': '#6a6a76',
    // Accents — solid + bright, straight off the painting.
    '--wash-accent-red': '#e62b1e', // bright red
    '--wash-accent-orange': '#ff6a00', // bright sun-orange
    '--wash-accent-amber': '#ffb20a', // bright golden sun
    '--wash-accent-lime': '#9bd61e',
    '--wash-accent-green': '#2ecc40', // bright emerald
    '--wash-accent-teal': '#00b3a4',
    '--wash-accent-cyan': '#1bc4e0', // bright turquoise sea
    '--wash-accent-blue': '#2a7fff', // electric ocean blue
    '--wash-accent-indigo': '#5a5af0',
    '--wash-accent-violet': '#a05cf0',
    '--wash-accent-magenta': '#e83fa0',
    '--wash-accent-pink': '#ff5fa0',
    // Chrome: black captions, near-white fg; a thin bright top edge on the
    // active caption marks focus.
    '--wash-titlebar-active': '#1a1a20',
    '--wash-titlebar-inactive': '#0c0c0f',
    '--wash-titlebar-fg': '#f1f1f5',
    // Start bar (taskbar): black.
    '--wash-taskbar-bg': '#0a0a0d',
    '--wash-taskbar-top': '#26262e',
    // Desktop info banner sits over the busy dot wallpaper → light text on a
    // themed darken+blur backdrop so it always reads.
    '--wash-banner-fg': '#f1f1f5',
    '--wash-banner-bg': 'rgba(0,0,0,0.42)',
    '--wash-banner-blur': '8px',
    // Wallpaper stops at the taskbar (window-exclusive-of-taskbar), framed
    // by an elegant 5px black border around the dot-painting.
    '--wash-wallpaper-extent': 'desktop',
    '--wash-wallpaper-border': '5px solid #000',
    // Rounded chrome to match Quicksand's geometry and the painting's dots.
    '--wash-radius-sm': '5px',
    '--wash-radius-md': '8px',
    '--wash-radius-lg': '12px',
    '--wash-radius-xl': '16px',
    // Quicksand for the chrome (bundled below).
    '--wash-font-sans': '"Quicksand", ui-rounded, "Segoe UI", system-ui, sans-serif',
  },
  wallpaper: 'wallpapers/dreamtime.svg',
  fonts: [
    {
      family: 'Quicksand',
      url: 'fonts/Quicksand-Regular.woff2',
      boldUrl: 'fonts/Quicksand-Bold.woff2',
    },
  ],
};

// Lisbon — the city from the river. A LIGHT pack: whitewash surfaces,
// cobalt ink for focus and selection (azulejo blue), ochre and terracotta
// from the facades and roofs. Square-ish corners like a cut tile. Active
// windows wear a cobalt titlebar (white text) so the focused window reads at
// a glance. The wallpaper — whitewashed houses and red roofs stacked up the
// hill to the castle, over the water, under open sky — is an image-model
// raster vectorized with a real gradient sky (design/packs/vectorize.py).
const lisbon: Pack = {
  id: 'lisbon',
  name: 'Lisbon',
  appearance: 'light',
  scheme: {
    '--wash-bg-window': '#f8f7f3', // tin-glaze white
    '--wash-bg-menu': '#eff2f7', // faint cobalt cast for menus / bars
    '--wash-bg-inset': '#e7ecf3', // sunken fields
    '--wash-bg-canvas': '#fcfcfa', // editor/terminal canvas
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#e4eaf4',
    '--wash-bg-row-selected': '#cbdaf0', // cobalt wash
    '--wash-bg-backdrop': 'rgba(12,28,64,0.35)',
    '--wash-bg-drop-target': '#d5e4f8',
    '--wash-border-menu': '#d4dbe7',
    '--wash-border-window': '#9fb0cc',
    '--wash-border-focus': '#1f4fa3', // cobalt
    '--wash-border-drop-target': '#1f4fa3',
    '--wash-fg': '#1a2333', // deep navy ink
    '--wash-fg-muted': '#4e5a6f',
    '--wash-fg-dim': '#7f8aa0',
    '--wash-bg-danger': '#f8ddd6',
    '--wash-border-danger': '#d48c7a',
    '--wash-fg-danger': '#a3301c', // terracotta-red
    '--wash-bg-success': '#dcebdd',
    '--wash-fg-success': '#2e6a3b',
    '--wash-bg-warning': '#f5e6c4',
    '--wash-fg-warning': '#855a0c', // ochre
    '--wash-bg-info': '#d9e5f6',
    '--wash-fg-info': '#1f4fa3',
    '--wash-bg-neutral': '#e4e8ef',
    '--wash-bg-denied': '#f3e4c8',
    '--wash-border-denied': '#c49440',
    '--wash-sev-error': '#b0281c',
    '--wash-sev-warn': '#855a0c',
    '--wash-sev-notice': '#1f4fa3',
    '--wash-sev-info': '#3a4456',
    '--wash-sev-debug': '#7f8aa0',
    '--wash-accent-red': '#c0392b',
    '--wash-accent-orange': '#c8643b', // terracotta roofs
    '--wash-accent-amber': '#c9921f', // ochre (the tiles' yellow)
    '--wash-accent-lime': '#7a9a3a',
    '--wash-accent-green': '#3f8a5a',
    '--wash-accent-teal': '#2a8a86',
    '--wash-accent-cyan': '#2b87b5', // Tagus
    '--wash-accent-blue': '#1f4fa3', // cobalt
    '--wash-accent-indigo': '#3c4fa8',
    '--wash-accent-violet': '#6d5aa8',
    '--wash-accent-magenta': '#a3427f',
    '--wash-accent-pink': '#c45a7c',
    // Cobalt caption, dusty-cobalt when inactive: both carry white text
    // (the one --wash-titlebar-fg serves both), so the inactive tone stays
    // dark enough to read rather than fading to the window white.
    '--wash-titlebar-active': '#1f4fa3',
    '--wash-titlebar-inactive': '#5d73a0',
    '--wash-titlebar-fg': '#ffffff',
    // App icons in the caption go white too: a cobalt or Tagus-blue accent
    // icon would vanish into the cobalt bar.
    '--wash-titlebar-icon': '#ffffff',
    // Taskbar: the window white, nearly solid over the busy tiles, with a
    // cobalt edge like a tile border.
    '--wash-taskbar-bg': '#f8f7f3',
    '--wash-taskbar-opacity': '92%',
    '--wash-taskbar-top': '#1f4fa3',
    // The info banner sits on the tiles: a glaze-white backdrop keeps the
    // ink text legible.
    '--wash-banner-bg': 'rgba(248,247,243,0.85)',
    '--wash-banner-blur': '6px',
    // A light theme wants a softer, cooler window shadow than the default.
    '--wash-window-shadow': '0 8px 28px rgba(16,36,84,0.24)',
    '--wash-shadow-menu': '0 6px 18px rgba(16,36,84,0.22)',
    // Cut-tile corners: barely softened.
    '--wash-radius-sm': '2px',
    '--wash-radius-md': '3px',
    '--wash-radius-lg': '4px',
    '--wash-radius-xl': '6px',
  },
  wallpaper: 'wallpapers/lisbon.svg',
};

// Isfahan — Safavid tilework at dusk. A DARK pack: lapis-night chrome, warm
// ivory text, turquoise faience for focus and selection, gold for the
// strapwork accents (the taskbar's top edge, the caption's icon). Softly
// rounded like a pointed arch's spring. The wallpaper — a mosque iwan with
// muqarnas vaulting over a reflecting pool — is an image-model raster
// vectorized with a real gradient sky (design/packs/vectorize.py).
const isfahan: Pack = {
  id: 'isfahan',
  name: 'Isfahan',
  appearance: 'dark',
  scheme: {
    '--wash-bg-window': '#111c38', // lapis night
    '--wash-bg-menu': '#0e1830',
    '--wash-bg-inset': '#0a1328',
    '--wash-bg-canvas': '#070d1d', // editor/terminal: near-black lapis
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#1a2a4e',
    '--wash-bg-row-selected': '#16566a', // deep turquoise
    '--wash-bg-backdrop': 'rgba(2,6,18,0.55)',
    '--wash-bg-drop-target': '#123d55',
    '--wash-border-menu': '#1f2e52',
    '--wash-border-window': '#2b3d68',
    '--wash-border-focus': '#3fc1c9', // turquoise
    '--wash-border-drop-target': '#3fc1c9',
    '--wash-fg': '#efe6d2', // warm ivory
    '--wash-fg-muted': '#b2aa95',
    '--wash-fg-dim': '#7f7a6c',
    '--wash-bg-danger': '#3d1519',
    '--wash-border-danger': '#b43c3a',
    '--wash-fg-danger': '#ff9283',
    '--wash-bg-success': '#0f3221',
    '--wash-fg-success': '#71d49c',
    '--wash-bg-warning': '#3a2c0c',
    '--wash-fg-warning': '#e8bb52',
    '--wash-bg-info': '#0d3142',
    '--wash-fg-info': '#72d6dc',
    '--wash-bg-neutral': '#1a2747',
    '--wash-bg-denied': '#3a2a10',
    '--wash-border-denied': '#9c7a2c',
    '--wash-sev-error': '#ff7d6d',
    '--wash-sev-warn': '#e8bb52',
    '--wash-sev-notice': '#72d6dc',
    '--wash-sev-info': '#d9d1bd',
    '--wash-sev-debug': '#7f7a6c',
    '--wash-accent-red': '#d8544b',
    '--wash-accent-orange': '#e07d3e', // saffron
    '--wash-accent-amber': '#d9a93f', // gold
    '--wash-accent-lime': '#a9c25a',
    '--wash-accent-green': '#45b072',
    '--wash-accent-teal': '#2fb5a5',
    '--wash-accent-cyan': '#3fc1c9', // turquoise faience
    '--wash-accent-blue': '#5f8fe0', // lapis, lifted to read on the night chrome
    '--wash-accent-indigo': '#6470d0',
    '--wash-accent-violet': '#9278d0',
    '--wash-accent-magenta': '#c95e9e',
    '--wash-accent-pink': '#e07d9e',
    // Caption: a lapis a step brighter than the window when focused; the
    // app icon in gold, like the strapwork.
    '--wash-titlebar-active': '#1c3166',
    '--wash-titlebar-inactive': '#111c38',
    '--wash-titlebar-fg': '#efe6d2',
    '--wash-titlebar-icon': '#d9a93f',
    '--wash-taskbar-bg': '#0a1328',
    '--wash-taskbar-top': '#d9a93f', // a gold rule along the bar
    '--wash-banner-bg': 'rgba(8,15,34,0.5)',
    '--wash-banner-blur': '8px',
    '--wash-radius-sm': '4px',
    '--wash-radius-md': '6px',
    '--wash-radius-lg': '8px',
    '--wash-radius-xl': '12px',
  },
  wallpaper: 'wallpapers/isfahan.svg',
};

// Moscow — painted onion domes in snow at dusk. A LIGHT pack: snow-cream
// surfaces, near-black brown ink, brick red for focus and the caption
// (white text), gold and emerald from the domes for accents. The wallpaper
// — a toy-like St Basil's cluster — is an image-model raster vectorized
// with a real gradient sky (design/packs/vectorize.py).
const moscow: Pack = {
  id: 'moscow',
  name: 'Moscow',
  appearance: 'light',
  scheme: {
    '--wash-bg-window': '#fbf7ef', // snow cream
    '--wash-bg-menu': '#f5eee0',
    '--wash-bg-inset': '#efe5d3',
    '--wash-bg-canvas': '#fffbf4',
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#f1e5d0',
    '--wash-bg-row-selected': '#f4d3c8', // brick wash
    '--wash-bg-backdrop': 'rgba(60,14,10,0.35)',
    '--wash-bg-drop-target': '#dbe9d6',
    '--wash-border-menu': '#e5d6bc',
    '--wash-border-window': '#c8a68a',
    '--wash-border-focus': '#b5372b', // brick red
    '--wash-border-drop-target': '#2f8a4a', // emerald, so a drop never reads as an error
    '--wash-fg': '#2a1712',
    '--wash-fg-muted': '#6b4d41',
    '--wash-fg-dim': '#9c8274',
    '--wash-bg-danger': '#f9d6d0',
    '--wash-border-danger': '#d98478',
    '--wash-fg-danger': '#a8201a',
    '--wash-bg-success': '#d9ecd6',
    '--wash-fg-success': '#2a6e3c',
    '--wash-bg-warning': '#f8e6bf',
    '--wash-fg-warning': '#8a5b00',
    '--wash-bg-info': '#d8e3f4',
    '--wash-fg-info': '#2456b3',
    '--wash-bg-neutral': '#eee4d2',
    '--wash-bg-denied': '#f4e1bf',
    '--wash-border-denied': '#c4902f',
    '--wash-sev-error': '#b3221a',
    '--wash-sev-warn': '#8a5b00',
    '--wash-sev-notice': '#2456b3',
    '--wash-sev-info': '#4a3a33',
    '--wash-sev-debug': '#9c8274',
    '--wash-accent-red': '#c0362c',
    '--wash-accent-orange': '#d9681f',
    '--wash-accent-amber': '#c9921a', // dome gold
    '--wash-accent-lime': '#7aa02a',
    '--wash-accent-green': '#2f8a4a', // emerald
    '--wash-accent-teal': '#1f8f84',
    '--wash-accent-cyan': '#2a8fb5',
    '--wash-accent-blue': '#2456b3', // cobalt stripe
    '--wash-accent-indigo': '#3f4aa8',
    '--wash-accent-violet': '#7a4aa8',
    '--wash-accent-magenta': '#b5357a',
    '--wash-accent-pink': '#d8577f',
    // Brick caption with white text; dusty brick when inactive (dark
    // enough that the shared white fg still reads).
    '--wash-titlebar-active': '#b5372b',
    '--wash-titlebar-inactive': '#8f5a50',
    '--wash-titlebar-fg': '#ffffff',
    '--wash-titlebar-icon': '#ffffff',
    '--wash-taskbar-bg': '#fbf7ef',
    '--wash-taskbar-opacity': '90%',
    '--wash-taskbar-top': '#b5372b',
    '--wash-banner-bg': 'rgba(251,247,239,0.78)',
    '--wash-banner-blur': '6px',
    '--wash-window-shadow': '0 8px 28px rgba(60,20,10,0.26)',
    '--wash-shadow-menu': '0 6px 18px rgba(60,20,10,0.22)',
    // Rounded like the domes.
    '--wash-radius-sm': '4px',
    '--wash-radius-md': '6px',
    '--wash-radius-lg': '9px',
    '--wash-radius-xl': '14px',
  },
  wallpaper: 'wallpapers/moscow.svg',
};

/** All built-in packs, in gallery order. Midnight is first / default. */
export const packs: Pack[] = [midnight, tokyo, seoul, copland, oslo, dreamtime, lisbon, isfahan, moscow];

export const defaultPackId = 'midnight';

/** Resolve a pack id to a Pack, falling back to the default. */
export function getPack(id?: string | null): Pack {
  return packs.find((p) => p.id === id) ?? packs.find((p) => p.id === defaultPackId)!;
}

// The union of every --wash-* var any pack sets. Clearing these before
// applying a pack means switching FROM a full scheme TO one that omits
// a var correctly reverts that var to its tokens.ts hex fallback
// instead of leaving a stale value behind.
const allSchemeVars = (): string[] => {
  const names = new Set<string>();
  for (const p of packs) for (const k of Object.keys(p.scheme)) names.add(k);
  return [...names];
};

/**
 * Apply a pack's color scheme to `el` (normally document.documentElement).
 * Clears every var any pack defines, then sets this pack's — so the
 * result is exactly the pack, with no residue from a prior one.
 */
export function applyScheme(el: HTMLElement, pack: Pack): void {
  for (const name of allSchemeVars()) el.style.removeProperty(name);
  for (const [name, value] of Object.entries(pack.scheme)) el.style.setProperty(name, value);
  // Appearance: drives content apps (terminal, editor) that carry their
  // own palette, plus native form controls / scrollbars via color-scheme.
  el.style.setProperty('--wash-appearance', pack.appearance);
  el.style.setProperty('color-scheme', pack.appearance);
  // Kick any bundled web fonts this pack references (fire-and-forget).
  loadPackFonts(pack);
}

/** The active pack appearance, read off document.documentElement (set by
 *  applyScheme). Defaults to 'dark' before any pack applies. */
export function washAppearance(): 'light' | 'dark' {
  if (typeof document === 'undefined') return 'dark';
  const v = getComputedStyle(document.documentElement).getPropertyValue('--wash-appearance').trim();
  return v === 'light' ? 'light' : 'dark';
}

/** Fire `cb` whenever the active pack's appearance changes (a pack with a
 *  different light/dark from the current one is applied). Observes the
 *  documentElement inline-style attribute that applyScheme writes.
 *  Returns an unsubscribe fn. */
export function onAppearanceChange(cb: (appearance: 'light' | 'dark') => void): () => void {
  if (typeof MutationObserver === 'undefined') return () => {};
  let last = washAppearance();
  const obs = new MutationObserver(() => {
    const now = washAppearance();
    if (now !== last) {
      last = now;
      cb(now);
    }
  });
  obs.observe(document.documentElement, { attributes: true, attributeFilter: ['style'] });
  return () => obs.disconnect();
}
