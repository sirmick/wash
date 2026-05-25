#!/usr/bin/env node
// wash demo server — one process owns :5180.
//
// Responsibilities (replaces the old vite + debug-server.mjs split):
//   1. HTTP front door for everything the demo page loads:
//        /                  → index.html (TinyEMU/RISC-V wash demo)
//        /v86.html          → v86/x86 demo
//        /src/*.ts          → vite-transpiled with HMR
//        /tinyemu/*         → static (kernel, rootfs, bbl, wasm…)
//        /vfsync/*          → proxy to bellard.org (vite proxy plugin)
//        /shell/*           → served from web/shell/dist
//   2. WebSocket `/ws` — bidirectional log/control bus.
//        Browser clients send { t: "log" | "stage" }.
//        Admin clients (CLI, future dev UI) send { t: "ctl" | "input" }.
//        Server fans out: browser → admins + stdout + rotating file;
//        admin → all browsers.
//   3. HTTP `POST /admin/{verb}` — same as admin WS but shell-script
//      friendly. `curl -X POST :5180/admin/reload` etc.
//   4. `/9p/*` — reserved namespace, 404 today; the slot for a 9p
//      server when we wire that transport later.
//
// Strategy: lean on vite's createServer({ middlewareMode: true }) so
// we keep HMR + the existing /vfsync proxy + TS transpilation without
// re-implementing any of it. The new code is the WS bus + admin path.

import { createServer as createHttpServer } from 'node:http';
import { WebSocketServer } from 'ws';
import { createServer as createViteServer } from 'vite';
import { appendFile, mkdir } from 'node:fs/promises';
import { dirname, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const DEMO_ROOT = resolve(__dirname, '..');

const PORT = Number(process.env.PORT || 5180);
const HOST = process.env.HOST || '0.0.0.0';
const LOG_FILE = process.env.WASH_LOG_FILE || '/tmp/wash-demo-server.log';

// ─────────────────────────── http server (must exist before vite) ──────
// We create the http.Server FIRST so we can hand it to vite's HMR
// transport via `server.hmr.server`. Without this, vite tries to bind
// its own ws on port 24678 — which (a) collides if two demo servers
// run side-by-side on different PORTs and (b) the client-side HMR
// probe to that port fails through any proxy, putting the page in a
// "server connection lost" reload loop.
const server = createHttpServer();

// ─────────────────────────── vite middleware ────────────────────────────
const vite = await createViteServer({
  root: DEMO_ROOT,
  server: {
    middlewareMode: true,
    host: HOST,
    hmr: { server },           // share our http server for HMR upgrades
  },
  appType: 'spa',
});

// ─────────────────────────── log sink ──────────────────────────────────
// Append every WS-routed log line to a file alongside the stdout echo,
// so an off-page consumer can `tail -F`. Best-effort — failures here
// must not break message routing.
await mkdir(dirname(LOG_FILE), { recursive: true }).catch(() => {});
async function logToFile(line) {
  try { await appendFile(LOG_FILE, line + '\n'); } catch { /* ignore */ }
}

function ts() {
  return new Date().toISOString().replace('T', ' ').slice(0, -1);
}

// ─────────────────────────── http request handler ──────────────────────
server.on('request', async (req, res) => {
  const url = req.url || '/';
  // Lightweight request log — first 80 chars of URL is plenty for
  // debugging without spamming on every static asset. Skip /src/* and
  // /node_modules/* (vite-transpiled deps are noisy and expected).
  if (!url.startsWith('/src/') && !url.startsWith('/node_modules/') && !url.startsWith('/@')) {
    process.stdout.write(`${ts()}  [http] ${req.method} ${url.slice(0, 80)} ← ${req.socket.remoteAddress}\n`);
  }

  // Legacy URL: `/tinyemu.html` was the demo entry before tinyemu was
  // promoted to default at `/`. Bookmarks/history pointing at the old
  // path hit a 403 from vite. 301 to `/` and drop the now-stale `url=`
  // query that defaulted to Bellard's buildroot cfg — fresh load picks
  // up `wash-riscv64.cfg` from the index inline-script default.
  if (url === '/tinyemu.html' || url.startsWith('/tinyemu.html?')) {
    res.writeHead(301, { Location: '/' });
    res.end();
    return;
  }

  // /9p/* — reserved future mount point. 404 today so a misrouted
  // request fails loudly rather than silently falling through to vite.
  if (url.startsWith('/9p/') || url === '/9p') {
    res.writeHead(404, { 'Content-Type': 'text/plain' });
    res.end('9p server not wired yet — see web/demo/server/server.mjs');
    return;
  }

  // HTTP admin path — POST /admin/<verb> with optional JSON body.
  // Convenience wrapper around the admin-role WS frames. Useful for
  // shell scripts that don't want to spawn a ws client.
  //   curl -X POST :5180/admin/reload
  //   curl -X POST :5180/admin/input -d 'ls -la\n'
  if (req.method === 'POST' && url.startsWith('/admin/')) {
    const verb = url.slice('/admin/'.length).split('?')[0];
    let body = '';
    req.on('data', c => { body += c; if (body.length > 4096) req.destroy(); });
    req.on('end', () => {
      if (verb === 'reload' || verb === 'reset' || verb === 'stop' || verb === 'run') {
        broadcast('browser', { t: 'ctl', verb });
      } else if (verb === 'input') {
        broadcast('browser', { t: 'input', data: body });
      } else {
        res.writeHead(400); res.end(`unknown verb: ${verb}\n`); return;
      }
      res.writeHead(200, { 'Content-Type': 'text/plain' });
      res.end('ok\n');
      process.stdout.write(`${ts()}  [admin-http] ${verb}\n`);
    });
    return;
  }

  // Everything else → vite middleware (static, HMR, proxy, SPA).
  vite.middlewares(req, res);
});

// ─────────────────────────── websocket bus ─────────────────────────────
const wss = new WebSocketServer({ noServer: true });

server.on('upgrade', (req, socket, head) => {
  // Only /ws is a websocket route; other upgrades (vite HMR) are routed
  // through vite's own ws server on its own path. Vite's HMR uses
  // `/__vite_hmr` — let vite handle anything starting with /__.
  const url = req.url || '';
  if (url === '/ws' || url.startsWith('/ws?')) {
    wss.handleUpgrade(req, socket, head, (ws) => {
      wss.emit('connection', ws, req);
    });
  } else {
    // Vite installed its own upgrade handlers; just leave socket alone.
    // If we destroyed here we'd kill HMR.
  }
});

const browsers = new Set();
const admins = new Set();

function broadcast(target, msg) {
  const data = JSON.stringify(msg);
  const set = target === 'browser' ? browsers : admins;
  for (const ws of set) {
    if (ws.readyState === 1) ws.send(data);
  }
}

wss.on('connection', (ws, req) => {
  let role = 'unknown';
  const peer = `${req.socket.remoteAddress}:${req.socket.remotePort}`;
  process.stdout.write(`${ts()}  [ws-connect] ${peer}\n`);

  ws.on('message', async (raw) => {
    let msg;
    try { msg = JSON.parse(raw.toString()); }
    catch { ws.send(JSON.stringify({ t: 'error', error: 'bad json' })); return; }

    // First frame from a client must declare its role.
    if (msg.t === 'hello') {
      role = msg.role === 'admin' ? 'admin' : 'browser';
      (role === 'admin' ? admins : browsers).add(ws);
      process.stdout.write(`${ts()}  [ws-hello] ${peer} role=${role} page=${msg.page||'?'}\n`);
      ws.send(JSON.stringify({ t: 'welcome', role, browserCount: browsers.size, adminCount: admins.size }));
      // Notify admins when a browser arrives, so a CLI tail user sees it.
      if (role === 'browser') broadcast('admin', { t: 'event', kind: 'browser-connected', page: msg.page });
      return;
    }

    // Browser → server: log/stage. Echo to stdout + log file + admins.
    if (role === 'browser' && (msg.t === 'log' || msg.t === 'stage')) {
      const line = formatBrowserMsg(msg);
      process.stdout.write(line + '\n');
      logToFile(line);
      broadcast('admin', msg);
      return;
    }

    // Admin → server: ctl/input. Forward to all browsers.
    if (role === 'admin' && (msg.t === 'ctl' || msg.t === 'input')) {
      broadcast('browser', msg);
      process.stdout.write(`${ts()}  [admin-ws] ${msg.t} ${msg.verb || JSON.stringify(msg.data).slice(0, 40)}\n`);
      return;
    }

    // Unknown frame from established role.
    process.stdout.write(`${ts()}  [ws-drop] role=${role} t=${msg.t}\n`);
  });

  ws.on('close', () => {
    browsers.delete(ws);
    admins.delete(ws);
    process.stdout.write(`${ts()}  [ws-close] ${peer} role=${role}\n`);
    if (role === 'browser') broadcast('admin', { t: 'event', kind: 'browser-disconnected' });
  });

  ws.on('error', (e) => {
    process.stdout.write(`${ts()}  [ws-error] ${peer} ${e.message}\n`);
  });
});

function formatBrowserMsg(msg) {
  if (msg.t === 'log') {
    const src = msg.source || 'log';
    return `${ts()}  [${src}] ${msg.line || ''}`;
  }
  if (msg.t === 'stage') {
    const state = msg.state ? `(${msg.state})` : '';
    return `${ts()}  [stage]${state} ${msg.label || ''}`;
  }
  return `${ts()}  [browser] ${JSON.stringify(msg)}`;
}

// ─────────────────────────── start ─────────────────────────────────────
server.listen(PORT, HOST, () => {
  process.stdout.write(`${ts()}  [server] http://${HOST}:${PORT}/  (ws /ws, admin /admin/*, log → ${LOG_FILE})\n`);
});

// Graceful shutdown so docker/dev-restart cycles don't leak sockets.
for (const sig of ['SIGINT', 'SIGTERM']) {
  process.on(sig, async () => {
    process.stdout.write(`${ts()}  [server] ${sig} — shutting down\n`);
    server.close();
    await vite.close();
    process.exit(0);
  });
}
