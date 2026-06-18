import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Library build: a single self-contained ES module that defines the
// <wash-app-connect> custom element. Shared deps are externalized and
// resolved at runtime via the shell's /vendor/* import map.
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
