import { defineConfig, type Plugin } from 'vite';
import { dirname, resolve, sep } from 'node:path';
import { fileURLToPath } from 'node:url';
import { existsSync, readFileSync, statSync } from 'node:fs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const shellDist = resolve(__dirname, '..', 'shell', 'dist');

// During vite dev, serve the wash shell bundle (built separately via
// `pnpm -F @wash/shell build`) at /shell/*. The demo page imports
// /shell/shell.js as a module — same path works in production
// because the build step copies the shell dist into demo/public/shell.
function serveShellDistMiddleware(): Plugin {
  const mimeFor = (path: string): string => {
    if (path.endsWith('.js')) return 'application/javascript';
    if (path.endsWith('.html')) return 'text/html';
    if (path.endsWith('.css')) return 'text/css';
    if (path.endsWith('.svg')) return 'image/svg+xml';
    if (path.endsWith('.json')) return 'application/json';
    return 'application/octet-stream';
  };
  return {
    name: 'wash-demo-serve-shell-dist',
    apply: 'serve',
    configureServer(server) {
      server.middlewares.use('/shell', (req, res, next) => {
        const url = (req.url || '/').split('?')[0];
        const candidate = resolve(shellDist, '.' + url);
        // Path traversal guard.
        if (!candidate.startsWith(shellDist + sep) && candidate !== shellDist) {
          res.statusCode = 403;
          res.end();
          return;
        }
        if (existsSync(candidate) && statSync(candidate).isFile()) {
          res.setHeader('Content-Type', mimeFor(candidate));
          // Disable any caching during dev so shell rebuilds show up
          // on a plain page reload.
          res.setHeader('Cache-Control', 'no-store');
          res.end(readFileSync(candidate));
          return;
        }
        next();
      });
    },
  };
}

export default defineConfig({
  plugins: [serveShellDistMiddleware()],
  server: {
    host: '0.0.0.0',
    port: 5180,
    strictPort: true,
    // fs.allow widens the file-serving root to the repo top so that:
    //   (a) requests with URL-encoded slashes in query strings
    //       (e.g. /?url=%2Ftinyemu%2Fwash-riscv64.cfg) aren't
    //       misinterpreted as path-traversal and 403'd by vite's
    //       middleware (the symptom we hit when promoting tinyemu to
    //       the default route);
    //   (b) future cross-package imports from web/shell/dist resolve
    //       without needing per-import allow entries.
    fs: {
      strict: false,
      allow: ['../..'], // wash repo root, relative to web/demo/
    },
    proxy: {
      // TinyEMU/jslinux 9p root for the side-by-side RISC-V demo.
      // vfsync.org restricts CORS to bellard.org; proxying through
      // the dev server bypasses that. /vfsync/<path> → vfsync.org.
      '/vfsync': {
        target: 'https://vfsync.org',
        changeOrigin: true,
        rewrite: (p) => p.replace(/^\/vfsync/, '/u/os'),
      },
    },
  },
  build: {
    outDir: 'dist',
    emptyOutDir: true,
    target: 'es2022',
    sourcemap: false,
    // The kernel + rootfs blobs in public/ are megabytes; keep them
    // as real files, never base64-inlined.
    assetsInlineLimit: 0,
  },
});
