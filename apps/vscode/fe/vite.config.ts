import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Builds the settings panel bundle (panel.js), not an app index.js: the
// vscode service has no window of its own — its control UI is a panel
// the settings app hosts (docs/SETTINGS.md).
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
