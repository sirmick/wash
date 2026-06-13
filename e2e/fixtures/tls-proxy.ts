// TLS-terminating reverse proxy for e2e — the same role nginx/Caddy
// plays in front of wash in production (the router itself speaks only
// plain HTTP; see internal/runner/login's --cookie-secure notes).
//
// Why it exists: browser clipboard behavior forks on the SECURE
// CONTEXT boundary. http://127.0.0.1 is already "potentially
// trustworthy", so the default e2e environment can never exercise the
// https+wss path a LAN deployment uses. This proxy serves the wash
// shell over real TLS so specs can assert the secure-context behavior
// (navigator.clipboard present, wss:// WebSocket through a
// terminator) against a production-shaped stack.
//
// The key/cert pair is a committed throwaway (CN "wash e2e
// throwaway", self-signed, ~100y expiry) — private key in-repo is
// fine because it authenticates nothing; browsers connect with
// ignoreHTTPSErrors.

import { createServer, type Server } from 'node:https';
import { request as httpRequest } from 'node:http';
import { connect } from 'node:net';
import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));

export interface TlsProxyHandle {
  /** https://localhost:<port> — pass to page.goto. */
  url: string;
  close(): Promise<void>;
}

// startTlsProxy terminates TLS on a fresh port and forwards both
// plain requests and WebSocket upgrades to `backendURL` (the router's
// http://127.0.0.1:<port>).
export async function startTlsProxy(backendURL: string): Promise<TlsProxyHandle> {
  const backend = new URL(backendURL);
  const srv: Server = createServer({
    key: readFileSync(join(here, 'tls-test-key.pem')),
    cert: readFileSync(join(here, 'tls-test-cert.pem')),
  });

  // Plain HTTP: replay the request against the backend, stream the
  // response back. The original Host header is PRESERVED (nginx's
  // `proxy_set_header Host $host` discipline) — the router's WS
  // same-origin guard compares the browser's Origin host against
  // Host, so a terminator that rewrites Host gets every WS handshake
  // 403'd. The router doesn't vhost, so routing doesn't care.
  srv.on('request', (req, res) => {
    const up = httpRequest(
      {
        host: backend.hostname,
        port: backend.port,
        path: req.url,
        method: req.method,
        headers: req.headers,
      },
      (upRes) => {
        res.writeHead(upRes.statusCode ?? 502, upRes.headers);
        upRes.pipe(res);
      },
    );
    up.on('error', () => {
      if (!res.headersSent) res.writeHead(502);
      res.end();
    });
    req.pipe(up);
  });

  // WebSocket: write the upgrade request to a raw TCP connection and
  // splice the sockets — after the 101 the bytes are opaque frames.
  // Host (and Origin) pass through untouched, same reason as above.
  srv.on('upgrade', (req, socket, head) => {
    const up = connect(Number(backend.port), backend.hostname, () => {
      let lines = `${req.method} ${req.url} HTTP/1.1\r\n`;
      for (let i = 0; i < req.rawHeaders.length; i += 2) {
        lines += `${req.rawHeaders[i]}: ${req.rawHeaders[i + 1]}\r\n`;
      }
      up.write(lines + '\r\n');
      if (head.length) up.write(head);
      up.pipe(socket);
      socket.pipe(up);
    });
    const drop = () => {
      up.destroy();
      socket.destroy();
    };
    up.on('error', drop);
    socket.on('error', drop);
    // pipe() doesn't destroy its counterpart on close; without this
    // a torn-down browser socket leaves the upstream half open.
    up.on('close', drop);
    socket.on('close', drop);
  });

  // srv.close() only stops the listener and then waits for every live
  // connection to end — with the shell's WebSocket still spliced
  // through, that's forever. Track sockets so close() can destroy them.
  const socks = new Set<import('node:net').Socket>();
  srv.on('secureConnection', (s) => {
    socks.add(s);
    s.on('close', () => socks.delete(s));
  });

  await new Promise<void>((resolve) => srv.listen(0, '127.0.0.1', resolve));
  const addr = srv.address();
  if (addr === null || typeof addr === 'string') throw new Error('tls proxy: no port');
  return {
    // "localhost" (not 127.0.0.1) so the cert's SAN matches either way;
    // the https scheme is what makes the origin a secure context.
    url: `https://localhost:${addr.port}`,
    close: () =>
      new Promise((resolve) => {
        srv.close(() => resolve());
        for (const s of socks) s.destroy();
      }),
  };
}
