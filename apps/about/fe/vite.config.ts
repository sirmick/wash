import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Library build: a single self-contained ES module that defines the
// <wash-app-about> custom element. Solid powers the rendering so
// state changes drive the UI without manual render calls.
export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    lib: {
      entry: 'src/main.tsx',
      formats: ['es'],
      fileName: () => 'index.js',
    },
    rollupOptions: {
      // Shared vendor modules resolved via /vendor/* import map in
      // web/shell/index.html. Keep in sync with build-vendor.mjs.
      external: [
        'solid-js',
        'solid-js/web',
        'solid-js/store',
        '@xterm/xterm',
        '@xterm/addon-fit',
        '@wash/ui',
      ],
    },
  },
});
