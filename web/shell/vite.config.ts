import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Specifiers externalized to the shared vendor bundles served from
// /vendor/* (see web/shell/build-vendor.mjs + index.html import map).
// The shell must keep these out of its own bundle too, otherwise the
// page ends up with two copies of solid-js and reactivity breaks.
const VENDOR_EXTERNAL = [
  'solid-js',
  'solid-js/web',
  'solid-js/store',
  '@xterm/xterm',
  '@xterm/addon-fit',
  '@wash/ui',
];

export default defineConfig({
  plugins: [solid()],
  // Dev server also needs to skip pre-bundling externals — without
  // this, Vite's optimizeDeps rewrites the bare specifiers and the
  // import map never sees them.
  optimizeDeps: { exclude: VENDOR_EXTERNAL },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    rollupOptions: {
      external: VENDOR_EXTERNAL,
      output: {
        // Pin entry name so the router serves a stable path.
        entryFileNames: 'shell.js',
        chunkFileNames: 'shell-[name].js',
        assetFileNames: 'shell-[name][extname]',
      },
    },
  },
});
