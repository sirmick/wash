// Per-test fixture for the wash-login front-door (docs/MULTIUSER.md).
//
// Spawns wash-login on a unique loopback port with a tmp run-root,
// a fresh HMAC secret, and --auth-test credentials. Tests use the
// returned handle to drive Playwright at the login URL and assert on
// the wash-login stderr buffer for handoff / spawn events.
//
// Cleanup is load-bearing: every spawned per-user wash-router is
// SIGTERM'd before the test exits so concurrent test runs don't pile
// up routers that hold inotify instances and trip fs.watch — see
// feedback_e2e_orphan_accumulation.

import { test as base } from '@playwright/test';
import { spawn, ChildProcess } from 'node:child_process';
import { mkdtempSync, existsSync, readdirSync, mkdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:net';
import { execSync } from 'node:child_process';

const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const REPO_ROOT = resolve(__dirname, '..', '..');
const LOGIN_BIN = join(REPO_ROOT, 'out', 'wash-login');
const ROUTER_BIN = join(REPO_ROOT, 'out', 'wash-router');
const SESSION_BIN = join(REPO_ROOT, 'out', 'wash-session');
const ABOUT_BIN = join(REPO_ROOT, 'out', 'wash-about');

export interface LoginHandle {
  /** Base URL of the wash-login HTTP server. */
  url: string;
  /** Tmp run-root where /run/wash/<uid>/sessions/<sessid>.sock land. */
  runRoot: string;
  /** Default --auth-test credentials this fixture uses. */
  user: string;
  password: string;
  /** Current uid as a string — convenience for assertions about
   *  /proc and /run paths. */
  uid: number;
  /** Captured stderr (auth attempts, spawn lines, handoff log). */
  log(): string;
  /** Wait until the captured stderr matches `re`, or timeout. */
  waitForLog(re: RegExp, timeoutMs?: number): Promise<string>;
  /** The wash-login process handle, for tests that need to inspect
   *  exit codes or send signals. */
  proc: ChildProcess;
}

export interface LoginOptions {
  /** Override the auth-test credentials. Default "alice:hunter2". */
  auth?: { user: string; password: string };
  /** Extra args appended to the wash-login command line. */
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

async function waitForRegex(getBuf: () => string, re: RegExp, timeoutMs: number): Promise<string> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const buf = getBuf();
    const m = buf.match(re);
    if (m) return m[0];
    await new Promise((r) => setTimeout(r, 50));
  }
  throw new Error(`timed out waiting for ${re}; log was:\n${getBuf()}`);
}

/**
 * Sweep runRoot looking for any wash-router process owned by `uid`
 * whose ctl socket lives somewhere under runRoot, and SIGTERM it.
 * Idempotent / safe to call repeatedly.
 */
function killSpawnedRoutersUnder(runRoot: string, uid: number) {
  // Walk /proc, match wash-router processes for this uid, check
  // argv for --listen-unix path starting with runRoot.
  let entries: string[];
  try {
    entries = readdirSync('/proc');
  } catch {
    return;
  }
  for (const e of entries) {
    if (!/^\d+$/.test(e)) continue;
    const pid = parseInt(e, 10);
    let statusOK = false;
    let argvLine = '';
    try {
      const statusPath = `/proc/${pid}/status`;
      const status = execSync(`cat ${statusPath}`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
      // Uid: real eff saved fs
      const m = status.match(/^Uid:\s+(\d+)/m);
      if (!m) continue;
      if (parseInt(m[1], 10) !== uid) continue;
      statusOK = true;
      const cmdline = execSync(`cat /proc/${pid}/cmdline`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
      argvLine = cmdline.replace(/\0/g, ' ');
    } catch {
      continue;
    }
    if (!statusOK) continue;
    if (!argvLine.includes('wash-router')) continue;
    if (!argvLine.includes(runRoot)) continue;
    try {
      process.kill(pid, 'SIGTERM');
    } catch {
      // already gone
    }
  }
}

export async function startLogin(opts: LoginOptions = {}): Promise<LoginHandle> {
  for (const b of [LOGIN_BIN, ROUTER_BIN, SESSION_BIN, ABOUT_BIN]) {
    if (!existsSync(b)) {
      throw new Error(`missing binary: ${b}\n(make from the repo root)`);
    }
  }

  const port = await freePort();
  const workDir = mkdtempSync(join(tmpdir(), 'wash-e2e-login-'));
  const runRoot = join(workDir, 'run');
  mkdirSync(runRoot, { recursive: true });
  const secretKey = join(workDir, 'secret.key');

  // wash-router needs siblings (wash-session, wash-about) findable
  // via apps-dir. Stage them into the work dir so the spawned router
  // can find them at its default discovery path.
  const appsDir = join(workDir, 'apps');
  mkdirSync(appsDir, { recursive: true });
  // Use symlinks instead of copies — the binaries are >4 MB each
  // and we don't need separate copies per test.
  for (const bin of [ROUTER_BIN, SESSION_BIN, ABOUT_BIN]) {
    const dest = join(appsDir, bin.split('/').pop()!);
    if (!existsSync(dest)) {
      execSync(`ln -s ${bin} ${dest}`);
    }
  }

  const user = opts.auth?.user ?? 'alice';
  const password = opts.auth?.password ?? 'hunter2';
  const uid = process.getuid?.() ?? 0;

  const args = [
    '--listen', `127.0.0.1:${port}`,
    // Plain HTTP: wash-login defaults to self-signed HTTPS, but the tests
    // drive it over http://127.0.0.1 (trusted loopback). HTTPS-by-default
    // is covered by the runner unit tests.
    '--http',
    '--secret-key', secretKey,
    '--secret-generate',
    '--auth-test', `${user}:${password}`,
    '--auth-test-uid', String(uid),
    // The username dropdown is populated from /etc/passwd, so it never
    // contains the synthetic --auth-test user. Hide the list so the
    // form renders a plain text <input name="user"> the tests can fill
    // — the auth + SCM_RIGHTS handoff under test are independent of the
    // user-picker UX.
    '--user-list', 'hide',
    '--router-binary', join(appsDir, 'wash-router'),
    '--apps-dir', appsDir,
    '--run-root', runRoot,
    ...(opts.extraArgs ?? []),
  ];

  // WASH_LOGIN_ENV=off: --listen-unix routers otherwise auto-adopt the
  // login-shell env (running the developer's real ~/.profile per spawned
  // session) — veto for determinism; wash-login passes it through.
  const proc = spawn(LOGIN_BIN, args, {
    stdio: ['ignore', 'pipe', 'pipe'],
    env: { ...process.env, WASH_LOGIN_ENV: 'off' },
  });
  let logBuf = '';
  proc.stdout.on('data', (c: Buffer) => { logBuf += c.toString('utf8'); });
  proc.stderr.on('data', (c: Buffer) => { logBuf += c.toString('utf8'); });
  const exitPromise: Promise<{ code: number | null; signal: NodeJS.Signals | null }> = new Promise((res) => {
    proc.once('exit', (code, signal) => res({ code, signal }));
  });
  await Promise.race([
    waitForRegex(() => logBuf, /listening on /, 5_000),
    exitPromise.then((r) => {
      throw new Error(`wash-login exited before listening: code=${r.code} signal=${r.signal}\n${logBuf}`);
    }),
  ]);

  return {
    url: `http://127.0.0.1:${port}/`,
    runRoot,
    user,
    password,
    uid,
    log: () => logBuf,
    waitForLog: (re, timeout = 5_000) => waitForRegex(() => logBuf, re, timeout),
    proc,
  };
}

export async function stopLogin(handle: LoginHandle) {
  // Kill any per-user routers wash-login spawned under our run-root
  // FIRST so they don't outlive wash-login if it crashes.
  killSpawnedRoutersUnder(handle.runRoot, handle.uid);

  try { handle.proc.kill('SIGTERM'); } catch { /* already gone */ }
  await new Promise<void>((res) => {
    const t = setTimeout(() => {
      try { handle.proc.kill('SIGKILL'); } catch { /* already gone */ }
      res();
    }, 2_000);
    handle.proc.once('exit', () => { clearTimeout(t); res(); });
  });

  // Second sweep — some routers may have noticed the parent's WS
  // drop only just now.
  killSpawnedRoutersUnder(handle.runRoot, handle.uid);
}

// Playwright fixture so individual tests can declare `login` as a
// parameter and get a fresh wash-login per test.
type Fixtures = {
  login: LoginHandle;
};

export const test = base.extend<Fixtures>({
  login: async ({}, use) => {
    const h = await startLogin();
    try {
      await use(h);
    } finally {
      await stopLogin(h);
    }
  },
});

export { expect } from '@playwright/test';
