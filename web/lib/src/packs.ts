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

// Tokyo Night — a deep blue-violet scheme. Second of the planned set;
// its palette is final, but it reuses Midnight's flower wallpaper until
// its own vectorized art lands (then point this at wallpapers/tokyo-night.svg).
const tokyoNight: Pack = {
  id: 'tokyo-night',
  name: 'Tokyo Night',
  scheme: {
    '--wash-bg-window': '#1a1b26',
    '--wash-bg-menu': '#16161e',
    '--wash-bg-inset': '#13141c',
    '--wash-bg-row': 'transparent',
    '--wash-bg-row-hover': '#292e42',
    '--wash-bg-row-selected': '#343a55',
    '--wash-bg-backdrop': 'rgba(0,0,0,0.5)',
    '--wash-bg-drop-target': '#2a3a5e',
    '--wash-border-menu': '#2a2e3f',
    '--wash-border-window': '#2a2e3f',
    '--wash-border-focus': '#3d59a1',
    '--wash-border-drop-target': '#7aa2f7',
    '--wash-fg': '#c0caf5',
    '--wash-fg-muted': '#565f89',
    '--wash-fg-dim': '#414868',
    '--wash-bg-danger': '#5a2230',
    '--wash-border-danger': '#8a3346',
    '--wash-fg-danger': '#f7768e',
    '--wash-bg-success': '#1f3a2e',
    '--wash-fg-success': '#9ece6a',
    '--wash-bg-warning': '#3a341c',
    '--wash-fg-warning': '#e0af68',
    '--wash-bg-info': '#1c2d4a',
    '--wash-fg-info': '#7aa2f7',
    '--wash-bg-neutral': '#20212e',
    '--wash-bg-denied': '#3a2a12',
    '--wash-border-denied': '#7a5a20',
    '--wash-sev-error': '#f7768e',
    '--wash-sev-warn': '#e0af68',
    '--wash-sev-notice': '#7dcfff',
    '--wash-sev-info': '#a9b1d6',
    '--wash-sev-debug': '#565f89',
    '--wash-accent-red': '#f7768e',
    '--wash-accent-amber': '#e0af68',
    '--wash-accent-green': '#9ece6a',
    '--wash-accent-cyan': '#7dcfff',
    '--wash-accent-blue': '#7aa2f7',
    '--wash-accent-violet': '#bb9af7',
  },
  wallpaper: 'wallpapers/midnight.svg',
};

/** All built-in packs, in gallery order. Midnight is first / default. */
export const packs: Pack[] = [midnight, tokyoNight];

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
