import { defineConfig } from 'vite';
import solid from 'vite-plugin-solid';

// Dev: `pnpm dev` (or `make dev`) runs Vite at :5173 with HMR. The
// shell still does `new WebSocket(/ws)` relative to its origin, so we
// proxy /ws to the live router at 127.0.0.1:11000. Prod is unaffected
// — the router serves the embedded build directly and there is no
// dev server in the loop.
export default defineConfig({
  plugins: [solid()],
  server: {
    port: 5173,
    strictPort: true,
    proxy: {
      '/ws': {
        target: 'ws://127.0.0.1:11000',
        ws: true,
      },
    },
  },
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
