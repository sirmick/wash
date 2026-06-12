import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// webamp (and its bundled React) is bundled INTO index.js — only the
// shell-provided runtimes (solid-js, @wash/ui) are externalized, same
// as every other wash app FE.
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
      external: ['solid-js', 'solid-js/web', 'solid-js/store', '@wash/ui'],
    },
  },
});
