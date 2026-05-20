import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Library build that bundles xterm + the fit addon + their CSS into a
// single ES module file. cssCodeSplit:false + the CSS-in-JS injection
// (see src/main.tsx) keep us to exactly one dist/index.js.
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
      external: [],
    },
  },
});
