// E2E for the auth-hardening pass (branch wash-auth-harden):
//   - /auth failure rate-limiting (lockout via the real login form)
//   - the /auth/check reconnect preflight
//   - the headline fix: an expired/cleared cookie surfaces as a bounce
//     to /login instead of an infinite "reconnecting…" spinner.
//
// These exercise browser-observable behaviour the Go unit tests can't:
// the form lockout path, and the FE reconnect loop's auth disambiguation
// driven by a real WebSocket drop.

import { test, expect } from '../fixtures/login';
import { startLogin, stopLogin } from '../fixtures/login';
import { readdirSync } from 'node:fs';
import { execSync } from 'node:child_process';

type Page = import('@playwright/test').Page;
type LoginHandle = import('../fixtures/login').LoginHandle;

// signInViaForm drives the visible login form and lands on the desktop
// at "/" (0 sessions → shell). Used by the reconnect test, which needs
// a live WS/desktop rather than the WS-free picker.
async function signInToDesktop(page: Page, login: LoginHandle) {
  await page.goto(login.url + 'login');
  await page.fill('input[name="user"]', login.user);
  await page.fill('input[name="password"]', login.password);
  await page.click('button[value="signin"]');
  await expect(page).toHaveURL(login.url);
}

test.describe('wash-login /auth rate limiting', () => {
  test('locks out after N failures, even with the right password', async ({ browser }) => {
    // Tiny threshold so the test is fast and deterministic; a long
    // window so the lockout doesn't lapse mid-test.
    const h = await startLogin({ extraArgs: ['--auth-max-fails', '2', '--auth-window', '1h'] });
    try {
      const ctx = await browser.newContext();
      const page = await ctx.newPage();

      const post = (password: string) =>
        page.request.post(h.url + 'auth', {
          form: { user: h.user, password, action: 'signin' },
          maxRedirects: 0,
        });

      // Two wrong attempts: each bounces back to /login?err=Invalid.
      for (let i = 0; i < 2; i++) {
        const r = await post('wrong');
        expect(r.status()).toBe(302);
        expect(r.headers()['location']).toMatch(/\/login\?err=Invalid/);
      }

      // Third attempt is throttled BEFORE auth — even the CORRECT
      // password is refused, with a too-many-attempts redirect and a
      // Retry-After header.
      const locked = await post(h.password);
      expect(locked.headers()['location']).toMatch(/Too\+many\+attempts/);
      expect(locked.headers()['retry-after']).toBeTruthy();

      // The server logged the throttle decision.
      await expect.poll(() => h.log(), { timeout: 5000 }).toMatch(/rate-limited/);

      await ctx.close();
    } finally {
      await stopLogin(h);
    }
  });
});

test.describe('wash-login /auth/check preflight', () => {
  test('401 (with login_url) when unauthed, 204 once authed', async ({ page, login }) => {
    const unauthed = await page.request.get(login.url + 'auth/check', { maxRedirects: 0 });
    expect(unauthed.status()).toBe(401);
    expect(await unauthed.json()).toMatchObject({ authenticated: false, login_url: '/login' });

    // Authenticate (request context sets the cookie without loading the
    // shell), then the preflight should flip to 204.
    const auth = await page.request.post(login.url + 'auth', {
      form: { user: login.user, password: login.password, action: 'signin' },
      maxRedirects: 0,
    });
    expect(auth.status()).toBe(302);

    const authed = await page.request.get(login.url + 'auth/check', { maxRedirects: 0 });
    expect(authed.status()).toBe(204);
  });
});

test.describe('wash-login reconnect legibility', () => {
  test('a dropped WS with no cookie bounces to /login, not an endless spinner', async ({ page, login }) => {
    // Real flow: sign in → desktop boots → WS drop → backoff → probe →
    // 800ms redirect. Legitimately longer than the tight 15s default.
    test.setTimeout(30000);

    await signInToDesktop(page, login);

    // The desktop boots and opens its WS, which spawns a per-user
    // router. Wait for the spawn log, then for at least one router to
    // appear under our run-root. (The desktop can momentarily open two
    // WS connections that both race the 0-session spawn branch, so we
    // don't assert an exact count — we just need something to kill.)
    await login.waitForLog(/ws: spawned new session sessid=s-[0-9a-f]+/, 10000);
    await expect.poll(() => findRouterPids(login.runRoot, login.uid).length, { timeout: 8000 })
      .toBeGreaterThanOrEqual(1);

    // Simulate the cookie having expired: clear it. The session
    // process keeps running — only the gateway's willingness to proxy
    // lapses, exactly as a real TTL expiry would.
    await page.context().clearCookies();

    // Sanity: with the cookie gone, the very probe the FE relies on
    // must 401. If this fails the bug is server-side, not in the shell.
    const probe = await page.request.get(login.url + 'auth/check', { maxRedirects: 0 });
    expect(probe.status(), 'auth/check should 401 once the cookie is cleared').toBe(401);

    // Force the live WS to drop by killing every per-user router. The
    // FE reconnect loop fires, re-dials /ws (now cookieless → 302),
    // and — because the handshake is refused, not the network —
    // probes /auth/check, gets 401, and redirects to /login instead
    // of spinning on "reconnecting…".
    for (const pid of findRouterPids(login.runRoot, login.uid)) {
      try { process.kill(pid, 'SIGTERM'); } catch { /* already gone */ }
    }

    await page.waitForURL(/\/login$/, { timeout: 15000 });
    await expect(page.locator('h1')).toContainText('sign in');
  });
});

// findRouterPids returns the pids of per-user wash-router processes
// whose --listen-unix socket lives under runRoot (this test's sandbox).
//
// Matching on --listen-unix is load-bearing: a substring match on
// "wash-router" would also hit wash-login itself, whose argv carries
// "--router-binary .../wash-router" and "--run-root .../run". Only the
// spawned per-user router is given --listen-unix, so keying on it (with
// the socket path under runRoot) selects exactly the routers — never
// the login front. (Mirrors login.spec.ts's findRunningRouterUnder.)
function findRouterPids(runRoot: string, uid: number): number[] {
  const out: number[] = [];
  let entries: string[];
  try {
    entries = readdirSync('/proc');
  } catch {
    return out;
  }
  for (const e of entries) {
    if (!/^\d+$/.test(e)) continue;
    const pid = parseInt(e, 10);
    try {
      const status = execSync(`cat /proc/${pid}/status`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
      const m = status.match(/^Uid:\s+(\d+)/m);
      if (!m || parseInt(m[1], 10) !== uid) continue;
      const args = execSync(`cat /proc/${pid}/cmdline`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).split('\0');
      if (!args.some((a) => a.endsWith('/wash-router') || a === 'wash-router')) continue;
      // The --listen-unix value (next arg, or after '=') must live under runRoot.
      let sock = '';
      for (let i = 0; i < args.length; i++) {
        if (args[i] === '--listen-unix' && i + 1 < args.length) { sock = args[i + 1]; break; }
        if (args[i].startsWith('--listen-unix=')) { sock = args[i].slice('--listen-unix='.length); break; }
      }
      if (sock && sock.startsWith(runRoot)) out.push(pid);
    } catch {
      continue;
    }
  }
  return out;
}
