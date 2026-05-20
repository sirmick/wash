// Build a single SVG sprite from lucide-static's icons. The sprite
// is served at /icons.svg by the router (it lives in shell/dist
// alongside index.html/shell.js, which the router embeds). The
// session app's chrome references icons via <use href="/icons.svg#name">.
//
// To add an icon: append its lucide name to ICONS below. To rebuild
// without rebuilding the shell, just `node build-icons.mjs`.

import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';

const __dirname = dirname(fileURLToPath(import.meta.url));
const require = createRequire(import.meta.url);

// Curated set of icons referenced by manifests + any catalog-side
// chrome. Hand-curated so the sprite stays small (no automatic
// bundling of all of Lucide's ~1500 icons).
const ICONS = [
  // App manifest icons.
  'folder',         // wash-fm
  'terminal',       // wash-term
  'info',           // wash-about
  'layout-dashboard', // wash-session (mostly hidden in catalog)
  'flask-conical',  // wash-test (hidden by default)
];

// Resolve via require so pnpm's nested layout is fine.
const lucideDir = dirname(require.resolve('lucide-static/package.json'));
const iconsDir = resolve(lucideDir, 'icons');

const symbols = ICONS.map((name) => {
  const path = resolve(iconsDir, `${name}.svg`);
  const svg = readFileSync(path, 'utf8');
  // Extract viewBox and inner content; re-wrap as a <symbol>.
  const m = svg.match(/<svg[\s\S]*?viewBox="([^"]+)"[\s\S]*?>([\s\S]*)<\/svg>/);
  if (!m) {
    throw new Error(`build-icons: malformed svg for ${name}: ${path}`);
  }
  // Strip the lucide HTML comment + collapse whitespace inside.
  const inner = m[2].replace(/<!--[\s\S]*?-->/g, '').trim();
  return `<symbol id="${name}" viewBox="${m[1]}">${inner}</symbol>`;
});

const sprite =
  `<?xml version="1.0" encoding="UTF-8"?>` +
  `<svg xmlns="http://www.w3.org/2000/svg" style="display:none">` +
  symbols.join('') +
  `</svg>`;

const outPath = resolve(__dirname, 'dist', 'icons.svg');
mkdirSync(dirname(outPath), { recursive: true });
writeFileSync(outPath, sprite);
console.log(`build-icons: wrote ${ICONS.length} symbols to ${outPath}`);
