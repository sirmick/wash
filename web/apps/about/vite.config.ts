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
      external: [],
    },
  },
});
