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
    '--wash-accent-amber': '#b58900', // yellow
    '--wash-accent-green': '#859900', // green
    '--wash-accent-cyan': '#2aa198', // cyan
    '--wash-accent-blue': '#268bd2', // blue
    '--wash-accent-violet': '#6c71c4', // violet
  },
  wallpaper: 'wallpapers/tokyo.svg',
};

/** All built-in packs, in gallery order. Midnight is first / default. */
export const packs: Pack[] = [midnight, tokyo];

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
}
