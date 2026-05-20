import { defineConfig } from 'vite';

// Library build: a single self-contained ES module that defines the
// wash-app-session custom element. No external imports — everything
// the element needs is bundled in (WIRE.md app bundle contract).
export default defineConfig({
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    lib: {
      entry: 'src/main.ts',
      formats: ['es'],
      fileName: () => 'index.js',
    },
    rollupOptions: {
      external: [], // bundle everything
    },
  },
});
