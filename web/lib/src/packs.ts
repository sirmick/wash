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

// NT — a 1990s workstation look (à la Windows NT, but ours): silver
// surfaces, white sunken fields, black text, a navy/white titlebar and a
// raised 3D window bevel (via the --wash-titlebar-* / --wash-border-light
// /dark override vars, which other packs leave unset), on a teal desktop.
// Accents are the classic VGA 16-color darks. A light theme.
const nt: Pack = {
  id: 'nt',
  name: 'NT',
  appearance: 'light',
  scheme: {
    '--wash-bg-window': '#c0c0c0', // silver
    '--wash-bg-menu': '#c0c0c0',
    '--wash-bg-inset': '#ffffff', // white sunken fields
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#d8d4cc',
    '--wash-bg-row-selected': '#8fa8c8', // steel-blue selection (black text still reads)
    '--wash-bg-backdrop': 'rgba(0,0,0,0.35)',
    '--wash-bg-drop-target': '#9ec0d8',
    '--wash-border-menu': '#808080',
    '--wash-border-window': '#808080',
    '--wash-border-focus': '#000080',
    '--wash-border-drop-target': '#000080',
    '--wash-fg': '#000000',
    '--wash-fg-muted': '#404040',
    '--wash-fg-dim': '#808080',
    '--wash-bg-danger': '#f4cccc',
    '--wash-border-danger': '#c00000',
    '--wash-fg-danger': '#a00000',
    '--wash-bg-success': '#a3d9a0', // clearer green — pops on the silver chrome
    '--wash-fg-success': '#085c08',
    '--wash-bg-warning': '#f4eec4',
    '--wash-fg-warning': '#808000',
    '--wash-bg-info': '#000080', // navy info badge…
    '--wash-fg-info': '#ffffff', // …with white text (Win9x selection look)
    '--wash-bg-neutral': '#e6e2da', // off-white, distinct from the silver window
    '--wash-bg-denied': '#f0e6c8',
    '--wash-border-denied': '#808000',
    '--wash-sev-error': '#c00000',
    '--wash-sev-warn': '#808000',
    '--wash-sev-notice': '#000080',
    '--wash-sev-info': '#404040',
    '--wash-sev-debug': '#808080',
    '--wash-accent-red': '#c00000',
    '--wash-accent-orange': '#c05000',
    '--wash-accent-amber': '#808000', // olive
    '--wash-accent-lime': '#408000',
    '--wash-accent-green': '#008000',
    '--wash-accent-teal': '#008060',
    '--wash-accent-cyan': '#008080', // teal
    '--wash-accent-blue': '#000080', // navy
    '--wash-accent-indigo': '#303090',
    '--wash-accent-violet': '#800080', // purple
    '--wash-accent-magenta': '#a000a0',
    '--wash-accent-pink': '#c04070',
    // Chrome override vars (only this pack sets them; window.tsx defaults
    // everywhere else): navy/white caption + raised bevel.
    '--wash-titlebar-active': '#000080',
    '--wash-titlebar-inactive': '#7f7f7f',
    '--wash-titlebar-fg': '#ffffff',
    // Window frame. The silver body already cuts a high-contrast edge
    // against the dark wallpaper, so a *dark* shadow border (the authentic
    // Win9x bottom-right) just reads as a stray line — and an inset bevel
    // gets painted over by the app's own background. So the frame is a
    // LIGHT bevel: bright highlight top-left, soft gray bottom-right, both
    // of which read on the dark wallpaper as a clean raised frame.
    '--wash-border-light': '#ffffff',
    '--wash-border-dark': '#d4d4d4',
    // Square 90s corners everywhere, and a gray taskbar (the default
    // sunken surface is white here, which looked wrong).
    '--wash-radius-sm': '0',
    '--wash-radius-md': '0',
    '--wash-radius-lg': '0',
    '--wash-radius-xl': '0',
    '--wash-taskbar-bg': '#c0c0c0',
    // Desktop info banner sits over the dark synthwave, so it needs light
    // text even though the chrome text is black.
    '--wash-banner-fg': '#e6e9ee',
    // Win9x-ish type + a raised light top edge on the taskbar (3D pop).
    '--wash-font-sans': 'Tahoma, "MS Sans Serif", Geneva, Verdana, sans-serif',
    '--wash-font-mono': '"Courier New", Courier, monospace',
    '--wash-taskbar-top': '#f4f4f4',
  },
  wallpaper: 'wallpapers/nt.svg',
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
  },
  wallpaper: 'wallpapers/oslo.svg',
};

/** All built-in packs, in gallery order. Midnight is first / default. */
export const packs: Pack[] = [midnight, tokyo, seoul, nt, oslo];

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
