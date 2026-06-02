import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Builds the settings "Display" panel bundle (panel.js) for the C++
// compositor. CMake embeds the output (fe/dist/panel.js) as raw bytes
// into the wash-display binary (docs/SETTINGS.md, docs/DISPLAY.md).
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
