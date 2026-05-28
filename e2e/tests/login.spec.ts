// E2E tests for the wash-login front-door (docs/MULTIUSER.md).
//
// Drives a real Chromium against a real wash-login binary, exercising
// the auth form + the SCM_RIGHTS handoff. The latter is the hard part
// — every other piece of wash-login has Go unit tests; only the full
// browser → wash-login → spawn-router → handoff flow lives here.

import { test, expect } from '../fixtures/login';
import { readdirSync, existsSync } from 'node:fs';
import { join } from 'node:path';
import { execSync } from 'node:child_process';

test.describe('wash-login auth flow', () => {
  test('unauthed visit redirects to /login', async ({ page, login }) => {
    await page.goto(login.url);
    await expect(page).toHaveURL(/\/login$/);
    await expect(page.locator('h1')).toContainText('log in');
    await expect(page.locator('input[name="user"]')).toBeVisible();
    await expect(page.locator('input[name="password"]')).toBeVisible();
  });

  test('bad credentials bounce back with error', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', 'wrong-password');
    await page.click('button[type="submit"]');
    await expect(page).toHaveURL(/\/login\?err=/);
    await expect(page.locator('.err')).toContainText('Invalid');
  });

  test('good credentials land on welcome page', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');
    // After 302 + 302 (auth → /, / → welcome page), the URL settles
    // on the root with the welcome page rendered.
    await expect(page).toHaveURL(login.url);
    await expect(page.locator('h1')).toContainText(`Authenticated as`);
    await expect(page.locator('h1 code')).toHaveText(login.user);
  });

  test('cookie survives reload', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');
    await expect(page.locator('h1 code')).toHaveText(login.user);

    await page.reload();
    await expect(page).toHaveURL(login.url);
    await expect(page.locator('h1 code')).toHaveText(login.user);
  });

  test('logout clears cookie and returns to /login', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');
    await expect(page.locator('h1 code')).toHaveText(login.user);

    await page.click('button[type="submit"]'); // welcome page's "Log out"
    await expect(page).toHaveURL(/\/login$/);

    // After logout, hitting / again should redirect to /login.
    await page.goto(login.url);
    await expect(page).toHaveURL(/\/login$/);
  });
});

test.describe('wash-login → wash-router handoff', () => {
  test('opening /ws after auth spawns a session and the WS reaches 101', async ({ page, login }) => {
    // Log in first so the browser context has the wash_session cookie.
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');
    await expect(page.locator('h1 code')).toHaveText(login.user);

    // Now open a WebSocket to /ws from the browser context. This
    // sends the cookie alongside the upgrade. wash-login validates,
    // scans /proc (0 sessions), spawns a per-user wash-router, and
    // SCM_RIGHTS-hands the fd to it. The router runs websocket.Accept
    // and writes the 101 back through. ws.onopen firing proves the
    // round trip works.
    const wsUrl = login.url.replace(/^http/, 'ws') + 'ws';
    const opened = await page.evaluate(async (url) => {
      return await new Promise<{ ok: boolean; readyState: number; message?: string }>((resolve) => {
        const ws = new WebSocket(url);
        const t = setTimeout(() => resolve({ ok: false, readyState: ws.readyState, message: 'timeout' }), 8000);
        ws.onopen = () => { clearTimeout(t); resolve({ ok: true, readyState: ws.readyState }); ws.close(); };
        ws.onerror = (e) => { clearTimeout(t); resolve({ ok: false, readyState: ws.readyState, message: String(e) }); };
      });
    }, wsUrl);

    expect(opened.ok).toBe(true);

    // Verify wash-login logged the spawn + handoff.
    await expect.poll(() => login.log(), { timeout: 5000 })
      .toMatch(/ws: spawned new session sessid=s-[0-9a-f]+ pid=\d+/);

    // The per-user wash-router process should now appear in /proc
    // owned by our uid with --listen-unix under the test's run-root.
    const found = findRunningRouterUnder(login.runRoot, login.uid);
    expect(found.length).toBe(1);
    expect(found[0].sock.startsWith(login.runRoot)).toBe(true);

    // The ctl socket file should exist as a Unix socket.
    expect(existsSync(found[0].sock)).toBe(true);
  });

  test('second WS to /ws attaches the existing session, not a new one', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');

    const wsUrl = login.url.replace(/^http/, 'ws') + 'ws';
    const open1 = await page.evaluate(async (url) => {
      return await new Promise<boolean>((resolve) => {
        const ws = new WebSocket(url);
        const t = setTimeout(() => resolve(false), 8000);
        ws.onopen = () => { clearTimeout(t); resolve(true); ws.close(); };
        ws.onerror = () => { clearTimeout(t); resolve(false); };
      });
    }, wsUrl);
    expect(open1).toBe(true);

    // Wait until wash-login logs the spawn so the next attach is
    // deterministically against the already-running session.
    await expect.poll(() => login.log(), { timeout: 5000 }).toMatch(/ws: spawned new session/);

    const open2 = await page.evaluate(async (url) => {
      return await new Promise<boolean>((resolve) => {
        const ws = new WebSocket(url);
        const t = setTimeout(() => resolve(false), 8000);
        ws.onopen = () => { clearTimeout(t); resolve(true); ws.close(); };
        ws.onerror = () => { clearTimeout(t); resolve(false); };
      });
    }, wsUrl);
    expect(open2).toBe(true);

    // The "attaching to existing" log line should be present.
    await expect.poll(() => login.log(), { timeout: 5000 })
      .toMatch(/ws: attaching to existing sessid=s-[0-9a-f]+/);

    // And only one per-user router should be running.
    const running = findRunningRouterUnder(login.runRoot, login.uid);
    expect(running.length).toBe(1);
  });

  test('logout?end_all=true terminates the spawned router', async ({ page, login }) => {
    await page.goto(login.url + 'login');
    await page.fill('input[name="user"]', login.user);
    await page.fill('input[name="password"]', login.password);
    await page.click('button[type="submit"]');

    // Open a WS to spawn a session.
    const wsUrl = login.url.replace(/^http/, 'ws') + 'ws';
    await page.evaluate(async (url) => {
      const ws = new WebSocket(url);
      await new Promise((r) => { ws.onopen = r; ws.onerror = r; });
      ws.close();
    }, wsUrl);

    await expect.poll(() => findRunningRouterUnder(login.runRoot, login.uid).length, { timeout: 5000 }).toBe(1);
    const before = findRunningRouterUnder(login.runRoot, login.uid);
    expect(before.length).toBe(1);
    const pid = before[0].pid;

    // Logout with end_all.
    await page.goto(login.url + 'logout?end_all=true');
    await expect(page).toHaveURL(/\/login$/);

    // Router should die within a couple of seconds.
    await expect.poll(() => processExists(pid), { timeout: 5000 }).toBe(false);

    // And /proc shouldn't see anything under our run-root anymore.
    expect(findRunningRouterUnder(login.runRoot, login.uid)).toEqual([]);
  });
});

interface FoundRouter {
  pid: number;
  sock: string;
}

function findRunningRouterUnder(runRoot: string, uid: number): FoundRouter[] {
  const out: FoundRouter[] = [];
  let entries: string[];
  try {
    entries = readdirSync('/proc');
  } catch {
    return out;
  }
  for (const e of entries) {
    if (!/^\d+$/.test(e)) continue;
    const pid = parseInt(e, 10);
    let argvLine = '';
    try {
      const status = execSync(`cat /proc/${pid}/status`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
      const m = status.match(/^Uid:\s+(\d+)/m);
      if (!m || parseInt(m[1], 10) !== uid) continue;
      const cmdline = execSync(`cat /proc/${pid}/cmdline`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
      argvLine = cmdline.replace(/\0/g, ' ');
    } catch {
      continue;
    }
    if (!argvLine.includes('wash-router')) continue;
    // Find the --listen-unix value.
    const args = argvLine.split(/\s+/);
    let sock = '';
    for (let i = 0; i < args.length; i++) {
      if (args[i] === '--listen-unix' && i + 1 < args.length) {
        sock = args[i + 1];
        break;
      }
      if (args[i].startsWith('--listen-unix=')) {
        sock = args[i].split('=', 2)[1];
        break;
      }
    }
    if (!sock || !sock.startsWith(runRoot)) continue;
    out.push({ pid, sock });
  }
  return out;
}

function processExists(pid: number): boolean {
  // /proc/<pid>/status is the authoritative answer; kill(pid, 0)
  // succeeds against zombies (which are "dead but not yet reaped")
  // and we want those treated as "gone."
  try {
    const status = execSync(`cat /proc/${pid}/status`, { encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] });
    const m = status.match(/^State:\s+(\w)/m);
    if (!m) return false;
    return m[1] !== 'Z';
  } catch {
    return false;
  }
}
