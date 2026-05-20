import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

export default defineConfig({
  plugins: [solid()],
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    rollupOptions: {
      output: {
        // Pin entry name so the router serves a stable path.
        entryFileNames: 'shell.js',
        chunkFileNames: 'shell-[name].js',
        assetFileNames: 'shell-[name][extname]',
      },
    },
  },
});
