// Shared library-build config for every wash app/panel FE: one self-contained
// ES module that defines the app's custom element, with the shared vendor
// modules externalized (resolved at runtime via the shell's /vendor/* import
// map — keep the external list in sync with web/shell/build-vendor.mjs).
// Per-app vite.config.ts files call this and override only what differs.
//
// opts:
//   entry        — lib entry (default 'src/main.tsx'; settings panels pass 'src/panel.tsx')
//   fileName     — output file (default 'index.js'; panels pass 'panel.js')
//   cssCodeSplit — pass false to inline CSS (wash-term, whose vendored xterm
//                  injects its own stylesheet on import)
//
// Listing @xterm/* as external is a no-op for apps that don't import it, so
// the list is uniform here rather than trimmed per app.

import solid from 'vite-plugin-solid';

export function washAppConfig(opts = {}) {
  const { entry = 'src/main.tsx', fileName = 'index.js', cssCodeSplit } = opts;
  const build = {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    lib: { entry, formats: ['es'], fileName: () => fileName },
    rollupOptions: {
      external: [
        'solid-js',
        'solid-js/web',
        'solid-js/store',
        '@xterm/xterm',
        '@xterm/addon-fit',
        '@wash/ui',
      ],
    },
  };
  if (cssCodeSplit !== undefined) build.cssCodeSplit = cssCodeSplit;
  return { plugins: [solid()], build };
}
