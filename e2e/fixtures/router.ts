// Per-test router fixture. Spawns the wash-router binary as a
// subprocess on a unique loopback port, captures its stderr into an
// in-memory buffer, exposes the URL + the log buffer to tests, and
// tears the process down on test end.
//
// Tests use this via the typed Playwright fixture exported below.

import { test as base, expect } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import { mkdtempSync, copyFileSync, existsSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';

// Repo root: e2e/fixtures/router.ts → up two.
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..');
const ROUTER_BIN = join(REPO_ROOT, 'out', 'wash-router');
const SESSION_BIN = join(REPO_ROOT, 'out', 'wash-session');
const ABOUT_BIN = join(REPO_ROOT, 'out', 'wash-about');
const TEST_BIN = join(REPO_ROOT, 'out', 'wash-test');
const TERM_BIN = join(REPO_ROOT, 'out', 'wash-term');
const LAUNCH_BIN = join(REPO_ROOT, 'out', 'wash-launch');

export interface RouterHandle {
  url: string;
  /** absolute path to wash-launch on this host (for tests that invoke it from a shell) */
  launchBin: string;
  /** the control socket path this router was started with */
  controlSocket: string;
  log(): string;
  waitForLog(pattern: RegExp, timeout?: number): Promise<string>;
  proc: ChildProcess;
}

export interface RouterOptions {
  /** kiosk mode: --no-session + --initial-app=<appID>. */
  kiosk?: string;
  /** include these binaries in the apps dir; defaults to all four. */
  apps?: ('session' | 'about' | 'test' | 'term')[];
  /** include manifest.hidden apps in the catalog. */
  showHidden?: boolean;
  /** extra wash-router args. */
  extraArgs?: string[];
}

async function freePort(): Promise<number> {
  return new Promise((resolveP, reject) => {
    const srv = createServer();
    srv.unref();
    srv.on('error', reject);
    srv.listen(0, '127.0.0.1', () => {
      const addr = srv.address();
      if (!addr || typeof addr === 'string') {
        reject(new Error('no address'));
        return;
      }
      const port = addr.port;
      srv.close(() => resolveP(port));
    });
  });
}

function stageApps(binaries: string[]): string {
  const dir = mkdtempSync(join(tmpdir(), 'wash-e2e-apps-'));
  for (const bin of binaries) {
    const dest = join(dir, bin.split('/').pop()!);
    copyFileSync(bin, dest);
    chmodSync(dest, 0o755);
  }
  return dir;
}

export async function startRouter(opts: RouterOptions = {}): Promise<RouterHandle> {
  for (const b of [ROUTER_BIN, SESSION_BIN, ABOUT_BIN, TEST_BIN, TERM_BIN, LAUNCH_BIN]) {
    if (!existsSync(b)) {
      throw new Error(`missing binary: ${b}\n(make TEST_APP=1 from the repo root)`);
    }
  }
  const wanted = opts.apps ?? ['session', 'about', 'test', 'term'];
  const bins: string[] = [];
  if (wanted.includes('session')) bins.push(SESSION_BIN);
  if (wanted.includes('about')) bins.push(ABOUT_BIN);
  if (wanted.includes('test')) bins.push(TEST_BIN);
  if (wanted.includes('term')) bins.push(TERM_BIN);
  const appsDir = stageApps(bins);

  const port = await freePort();
  // Each test gets its own control-socket path so concurrent test
  // runs don't trample each other.
  const controlSocket = join(appsDir, 'control.sock');
  const args = [
    '--listen',
    `127.0.0.1:${port}`,
    '--apps-dir',
    appsDir,
    '--control-socket',
    controlSocket,
  ];
  if (opts.kiosk) {
    args.push('--no-session', `--initial-app=${opts.kiosk}`);
  }
  if (opts.showHidden) {
    args.push('--show-hidden');
  }
  if (opts.extraArgs) args.push(...opts.extraArgs);

  const proc = spawn(ROUTER_BIN, args, { stdio: ['ignore', 'pipe', 'pipe'] });
  let logBuf = '';
  proc.stdout.on('data', (chunk: Buffer) => {
    logBuf += chunk.toString('utf8');
  });
  proc.stderr.on('data', (chunk: Buffer) => {
    logBuf += chunk.toString('utf8');
  });
  const exitPromise: Promise<{ code: number | null; signal: NodeJS.Signals | null }> = new Promise((res) => {
    proc.once('exit', (code, signal) => res({ code, signal }));
  });

  // Wait for the "listening on" line, or for the process to die.
  await Promise.race([
    waitForRegex(() => logBuf, /listening on /, 5_000),
    exitPromise.then((r) => {
      throw new Error(`wash-router exited before listening: code=${r.code} signal=${r.signal}\n${logBuf}`);
    }),
  ]);

  return {
    url: `http://127.0.0.1:${port}/`,
    launchBin: LAUNCH_BIN,
    controlSocket,
    log: () => logBuf,
    waitForLog: (re, timeout = 5_000) => waitForRegex(() => logBuf, re, timeout),
    proc,
  };
}

async function waitForRegex(read: () => string, re: RegExp, timeout: number): Promise<string> {
  const deadline = Date.now() + timeout;
  for (;;) {
    const m = read().match(re);
    if (m) return m[0];
    if (Date.now() > deadline) {
      throw new Error(`timed out waiting for ${re}\nlog so far:\n${read()}`);
    }
    await new Promise((r) => setTimeout(r, 50));
  }
}

export async function stopRouter(h: RouterHandle): Promise<void> {
  h.proc.kill('SIGTERM');
  await new Promise<void>((resolveP) => {
    const t = setTimeout(() => {
      try {
        h.proc.kill('SIGKILL');
      } catch {
        /* ignore */
      }
      resolveP();
    }, 1_500);
    h.proc.once('exit', () => {
      clearTimeout(t);
      resolveP();
    });
  });
}

// Typed fixtures: any test can pull a `router` from the function args
// and it'll be auto-spawned/torn down. Defaults to chrome mode with
// all three apps. Override via test.use({ kiosk: 'com.wash.test' }).
type Fixtures = {
  routerOpts: RouterOptions;
  router: RouterHandle;
};

export const test = base.extend<Fixtures>({
  routerOpts: [{}, { option: true }],
  router: async ({ routerOpts }, use) => {
    const h = await startRouter(routerOpts);
    try {
      await use(h);
    } finally {
      await stopRouter(h);
    }
  },
});

export { expect };
