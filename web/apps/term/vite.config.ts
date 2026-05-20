import { defineConfig } from 'vite';

// Library build that bundles xterm + the fit addon + their CSS into a
// single ES module file. cssCodeSplit:false + the CSS-in-JS injection
// (see src/main.ts) keep us to exactly one dist/index.js.
export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    cssCodeSplit: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['es'],
      fileName: () => 'index.js',
    },
    rollupOptions: {
      external: [],
    },
  },
});
