import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';
import { resolve } from 'node:path';

// Library build for the shared @wash/ui vendor bundle. Uses
// vite-plugin-solid (which wires babel-preset-solid) — esbuild's
// JSX transforms produce React-style jsx() calls that Solid can't
// consume, so this can't move into web/shell/build-vendor.mjs.
//
// Output lands in web/shell/public/vendor/wash-ui.js so the shell's
// dev server + prod build both ship it at /vendor/wash-ui.js. The
// import map in web/shell/index.html points @wash/ui there.
//
// solid-js + subpaths stay external — they're served from their own
// vendor bundles, and a single live Solid instance across the page
// is load-bearing (signal owner + instanceof checks).
export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: resolve(__dirname, '..', 'shell', 'public', 'vendor'),
    emptyOutDir: false,
    target: 'es2022',
    sourcemap: false,
    lib: {
      entry: 'src/index.ts',
      formats: ['es'],
      fileName: () => 'wash-ui.js',
    },
    rollupOptions: {
      external: ['solid-js', 'solid-js/web', 'solid-js/store'],
    },
  },
});
