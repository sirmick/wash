// asset URL helper — accounts for the page's deployment base path.
//
// wash chrome (icons.svg, wash-logo.svg) is conventionally served at
// the page's root: `/icons.svg`, `/wash-logo.svg`. That works for the
// standalone wash-router (page at `/`) and for wash-login (also at
// `/`), but breaks when the whole demo is hosted under a subpath like
// GitHub Pages' `/wash/` — the absolute path then 404s because the
// real chrome lives at `/wash/icons.svg`, not `/icons.svg`.
//
// The hosting HTML can publish its base path before any bundle loads:
//
//     <script>window.__washAssetBase = '/wash/';</script>
//
// Bundles read it via `washAssetUrl(name)` and get back the correct
// origin-rooted URL. When `__washAssetBase` is unset (the common case
// for wash-router / wash-login serving at `/`) the helper falls back
// to `/<name>` — same behavior as the hard-coded paths it replaces.
//
// One subtlety: this is a function (not a module-load-time constant)
// so the host page can set `window.__washAssetBase` *after* the
// bundle has been parsed but before the first asset render. The
// extra closure cost is unmeasurable vs the cost of an SVG fetch.

/**
 * Build an absolute URL for a piece of wash chrome (icons.svg,
 * wash-logo.svg) that's correct under the page's deployment base.
 *
 * Pass the path WITHOUT a leading slash (`icons.svg`, not
 * `/icons.svg`). The fragment, if any, is appended verbatim.
 *
 * Examples:
 *
 *     washAssetUrl('icons.svg')              // → '/icons.svg' (default)
 *     washAssetUrl('icons.svg#window')       // → '/icons.svg#window'
 *     washAssetUrl('wash-logo.svg')          // → '/icons.svg'
 *
 *     // With window.__washAssetBase = '/wash/':
 *     washAssetUrl('icons.svg#window')       // → '/wash/icons.svg#window'
 */
export function washAssetUrl(path: string): string {
  const base = (typeof window !== 'undefined' && (window as { __washAssetBase?: string }).__washAssetBase) || '/';
  return base + path.replace(/^\//, '');
}
