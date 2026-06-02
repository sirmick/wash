import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Builds the settings "Network" panel bundle (panel.js). netd is a
// windowless background service; this is the panel the settings app
// hosts (docs/SETTINGS.md, docs/NET.md §2.7).
export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    lib: {
      entry: 'src/panel.tsx',
      formats: ['es'],
      fileName: () => 'panel.js',
    },
    rollupOptions: {
      external: ['solid-js', 'solid-js/web', 'solid-js/store', '@wash/ui'],
    },
  },
});
