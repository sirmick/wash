// Build the shared vendor bundles served at /vendor/* by the router.
//
// Why: every app + the shell used to bundle its own copy of solid-js
// (~25 KB minified each) and the two terminal-using apps bundled
// their own ~300 KB copy of xterm. With externals + import maps,
// every consumer resolves the bare specifiers to these files instead.
//
// Output goes to web/shell/public/vendor/, which Vite's dev server
// serves at /vendor/* automatically and which the production build
// copies into web/shell/dist/ → cmd/wash-router/assets/.
//
// The import map in web/shell/index.html must stay in sync with the
// entries built here (and so must the externals lists in every app
// vite.config.ts). New shared dep → new entry here, new import-map
// row, new external in consumers.

import { mkdirSync, readFileSync } from 'node:fs';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createRequire } from 'node:module';
import { build } from 'esbuild';

const __dirname = dirname(fileURLToPath(import.meta.url));
const req = createRequire(import.meta.url);
const outDir = resolve(__dirname, 'public', 'vendor');
mkdirSync(outDir, { recursive: true });

const shared = {
  bundle: true,
  format: 'esm',
  // platform=browser picks the "browser" export condition + skips
  // Node built-in shimming. Without it, esbuild running under Node
  // resolves solid-js's "node" condition (-> dist/server.js), which
  // omits the browser-only exports (For, Show, render, …) and the
  // bundle ends up missing half the API.
  platform: 'browser',
  target: 'es2022',
  minify: true,
  legalComments: 'none',
  logLevel: 'warning',
};

// Exact-match external plugin. esbuild's `external: ['solid-js']`
// also matches every `solid-js/<sub>` specifier, which would leave
// our solid-web + solid-store entries as one-line re-exports rather
// than bundles. This plugin marks ONLY the listed specifiers
// external (exact match), so the entry's own imports of those names
// stay unbundled while subpath entries themselves get bundled.
const externalExact = (names) => ({
  name: 'external-exact',
  setup(b) {
    const set = new Set(names);
    b.onResolve({ filter: /.*/ }, (args) => {
      if (set.has(args.path)) return { path: args.path, external: true };
      return null;
    });
  },
});

// Resolve entry points relative to web/shell. We pass the bare
// specifier (not a Node-resolved path) so esbuild's own resolver
// honors `platform: 'browser'` + the package's "browser" export
// condition — Node's createRequire.resolve picks the "node"
// condition and would hand us dist/server.cjs (no <For>, no render).
// The shell's package.json depends on every vendor spec, so pnpm
// symlinks them under shell/node_modules — making this directory
// the right resolveDir for esbuild's resolver.
const entryResolveDir = __dirname;

// solid-js core. Self-contained — every other vendor bundle that
// touches Solid (solid-web, solid-store, @wash/ui) marks `solid-js`
// external so the browser resolves it through the import map to
// /vendor/solid.js. A single live Solid instance across the page is
// load-bearing: signal owner + instanceof checks all break if two
// copies of solid-js end up loaded.
const solidEntry = (specifier, out) => ({
  ...shared,
  resolveExtensions: ['.js', '.mjs', '.ts', '.tsx'],
  stdin: {
    contents: `export * from ${JSON.stringify(specifier)};`,
    resolveDir: entryResolveDir,
    sourcefile: `${out}.entry.js`,
  },
  outfile: resolve(outDir, `${out}.js`),
});

await build(solidEntry('solid-js', 'solid'));

await build({
  ...solidEntry('solid-js/web', 'solid-web'),
  plugins: [externalExact(['solid-js'])],
});

await build({
  ...solidEntry('solid-js/store', 'solid-store'),
  plugins: [externalExact(['solid-js'])],
});

// xterm + addon-fit. xterm ships UMD (lib/xterm.js); esbuild converts
// to ESM. The CSS that xterm needs is inlined into the bundle via a
// banner that injects a <style> tag on first import. Apps no longer
// need `?inline` imports of xterm.css — the runtime handles it.
const xtermPkgDir = dirname(req.resolve('@xterm/xterm/package.json'));
const xtermCSS = readFileSync(resolve(xtermPkgDir, 'css', 'xterm.css'), 'utf8');

// xterm ships as a webpack-built UMD whose Terminal export is
// assigned via runtime IIFE — esbuild's CJS→ESM bridge can't see it
// statically, so `export * from '@xterm/xterm'` produces an empty
// vendor bundle. Round-trip through the default import and re-
// export the public API explicitly. Symbols match the .d.ts.
const XTERM_NAMED = ['Terminal'];
await build({
  ...shared,
  stdin: {
    // Namespace import gives esbuild's CJS→ESM bridge the full
    // module.exports object (default-import bridges sometimes lose
    // it for webpack UMDs whose module.exports is the IIFE return).
    contents:
      `import * as xterm from '@xterm/xterm';\n` +
      XTERM_NAMED.map((n) => `export const ${n} = xterm.${n};`).join('\n'),
    resolveDir: entryResolveDir,
    sourcefile: 'xterm.entry.js',
  },
  outfile: resolve(outDir, 'xterm.js'),
  banner: {
    js:
      '(function(){if(typeof document==="undefined")return;' +
      'if(document.getElementById("__wash_xterm_css__"))return;' +
      'var s=document.createElement("style");' +
      's.id="__wash_xterm_css__";' +
      's.textContent=' +
      JSON.stringify(xtermCSS) +
      ';document.head.appendChild(s);})();',
  },
});

// Addon UMDs follow the same pattern as xterm itself.
const XTERM_FIT_NAMED = ['FitAddon'];
await build({
  ...shared,
  plugins: [externalExact(['@xterm/xterm'])],
  stdin: {
    contents:
      `import * as fit from '@xterm/addon-fit';\n` +
      XTERM_FIT_NAMED.map((n) => `export const ${n} = fit.${n};`).join('\n'),
    resolveDir: entryResolveDir,
    sourcefile: 'xterm-fit.entry.js',
  },
  outfile: resolve(outDir, 'xterm-fit.js'),
});

// @wash/ui is *not* built here — its Solid JSX requires the
// babel-preset-solid transform that vite-plugin-solid provides.
// See web/lib/vite.config.ts; that build writes wash-ui.js into
// this same /vendor/ output dir.

console.log(`build-vendor: wrote bundles to ${outDir}`);
