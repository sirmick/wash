// wash-top — process monitor. Three layers:
//
//   1. BE-level: details + signal handlers respond correctly over
//      the control socket. Signal is a real syscall — we spawn a
//      sentinel sleep proc, ask wash-top to TERM it, assert it dies.
//   2. Browser-level: launching wash-top via the palette renders the
//      proc table + CPU/mem chrome.
//   3. Permission classification: signaling pid 1 from the test
//      runner (non-root) returns permission_denied, not signal_failed.
//
// instancing=multi means each launch yields a fresh BE; we still
// reuse a single instance per test rather than spinning up many.

import { test, expect } from '../fixtures/router';
import { spawn } from 'node:child_process';

test.use({
  routerOpts: {
    apps: ['session', 'about', 'top'],
  },
});

// spawnSentinel: a sleeping child we can ask wash-top to kill.
// Returns the pid + a promise that resolves with the exit signal.
function spawnSentinel(): { pid: number; exited: Promise<NodeJS.Signals | null> } {
  // Use `sleep` rather than node so the proc is cheap and has a
  // distinct comm name. Detach so the test runner doesn't try to
  // own its lifecycle (it'll be killed by wash-top, not by us).
  const proc = spawn('/bin/sleep', ['600'], { stdio: 'ignore', detached: true });
  proc.unref();
  if (!proc.pid) throw new Error('sentinel: spawn failed');
  const exited = new Promise<NodeJS.Signals | null>((res) => {
    proc.once('exit', (_code, signal) => res(signal));
  });
  return { pid: proc.pid, exited };
}

test.describe('wash-top BE', () => {
  test.setTimeout(15_000);

  test('details handler returns /proc data for a live pid', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.top' });
    expect(launch.t).toBe('launched');
    const instance = launch.instance_id as string;

    // Use this test process's own pid — guaranteed live, guaranteed
    // readable, and present in /proc.
    const reply = await router.sendAppMsg(instance, {
      kind: 'details',
      id: 'd1',
      pid: process.pid,
    });
    expect(reply.kind).toBe('details_ok');
    expect(reply.pid).toBe(process.pid);
    expect(reply.found).toBe(true);
    // node's argv shows up in /proc/<pid>/cmdline; comm is "node".
    expect(typeof reply.comm).toBe('string');
    expect((reply.comm as string).length).toBeGreaterThan(0);
  });

  test('signal handler TERMs a sentinel process', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.top' });
    const instance = launch.instance_id as string;

    const sentinel = spawnSentinel();
    try {
      const reply = await router.sendAppMsg(instance, {
        kind: 'signal',
        id: 's1',
        pid: sentinel.pid,
        sig: 'TERM',
      });
      expect(reply.kind).toBe('signal_ok');
      expect(reply.pid).toBe(sentinel.pid);
      expect(reply.sig).toBe('TERM');

      const sig = await Promise.race([
        sentinel.exited,
        new Promise<NodeJS.Signals | null>((_, rej) =>
          setTimeout(() => rej(new Error('sentinel did not exit')), 5_000),
        ),
      ]);
      expect(sig).toBe('SIGTERM');
    } finally {
      // Belt-and-braces — if the wash-top signal didn't land for
      // some reason, don't leak a sleep(600) on the dev box.
      try { process.kill(sentinel.pid, 'SIGKILL'); } catch { /* gone */ }
    }
  });

  test('signal handler maps EPERM to permission_denied', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.top' });
    const instance = launch.instance_id as string;

    // pid 1 (systemd / init) is not signalable by a non-root test
    // runner. The classifier should map EPERM → "permission_denied".
    const reply = await router.sendAppMsg(instance, {
      kind: 'signal',
      id: 's2',
      pid: 1,
      sig: 'TERM',
    });
    expect(reply.kind).toBe('signal_err');
    expect(reply.code).toBe('permission_denied');
  });

  test('unknown signal name → bad_signal', async ({ router }) => {
    const launch = await router.controlRequest({ t: 'launch', app_id: 'com.wash.top' });
    const instance = launch.instance_id as string;

    const reply = await router.sendAppMsg(instance, {
      kind: 'signal',
      id: 's3',
      pid: process.pid,
      sig: 'BOGUS',
    });
    expect(reply.kind).toBe('signal_err');
    expect(reply.code).toBe('bad_signal');
  });
});

test.describe('wash-top browser', () => {
  test.setTimeout(20_000);

  test('palette → wash-top renders meters + at least one proc row', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.keyboard.press('Control+Space');
    const input = page.locator('[data-testid="palette-input"]');
    await expect(input).toBeFocused();
    await input.fill('system');
    await expect(page.locator('[data-testid="palette-item-com.wash.top"]')).toBeVisible();
    await page.keyboard.press('Enter');

    const topEl = page.locator('wash-app-top');
    await expect(topEl).toBeVisible();

    // Snapshot stream produces the chrome meters + at least one row
    // within a couple of ticks (default interval 2s; first push is
    // immediate).
    await expect(topEl.locator('[data-testid="top-cpu-meter"]')).toBeVisible({ timeout: 5_000 });
    await expect(topEl.locator('[data-testid="top-statusbar"]')).toBeVisible();
    await expect.poll(
      () => topEl.locator('[data-testid^="top-row-"]').count(),
      { timeout: 6_000 },
    ).toBeGreaterThan(0);
  });
});
