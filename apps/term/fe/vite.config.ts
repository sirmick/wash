import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Library build for the <wash-app-term> element. xterm + addon-fit
// are externalized (router serves them at /vendor/xterm.js +
// /vendor/xterm-fit.js); the vendor xterm.js auto-injects its CSS
// on import so the app no longer needs `?inline` CSS imports.
export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    cssCodeSplit: false,
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
