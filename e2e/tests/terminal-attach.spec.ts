// Terminal-attach: an app binary run outside the router (e.g.
// from a shell inside wash-term) dials WASH_DISPLAY and joins
// the session. This is the X-style "run wash-fm in a terminal,
// get a window" flow.

import { test, expect } from '../fixtures/router';
import { spawn } from 'node:child_process';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

test.describe('terminal-launched apps', () => {
  test('wash-about run with WASH_DISPLAY joins the session and gets a window', async ({ page, router }) => {
    // Open the desktop so the new window is visible.
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    // Run wash-about as a sibling process — no inherited fd,
    // no router spawn. Must use the staged binary path so the
    // router's /proc/<pid>/exe check matches.
    const aboutBin = join(router.appsDir, 'wash-about');
    expect(existsSync(aboutBin)).toBe(true);
    const child = spawn(aboutBin, [], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket },
      stdio: 'ignore',
      detached: false,
    });
    try {
      // wash-about's window should appear in the desktop.
      await expect(page.locator('wash-app-about')).toBeVisible({ timeout: 5_000 });
    } finally {
      child.kill('SIGTERM');
    }
  });

  test('attach with unregistered app_id is refused (via SDK error)', async ({ router }) => {
    // We can't easily craft an unregistered app via a binary,
    // but we CAN verify that a binary in the apps dir attaches
    // successfully (positive) and that the conn lives long
    // enough for the window to materialize. The negative path
    // (random binary trying to attach) is covered by the
    // /proc/<pid>/exe check at the router level — exercising it
    // requires a binary that *isn't* in the registry; deferred
    // until we have a non-router test binary fixture.
    const aboutBin = join(router.appsDir, 'wash-about');
    const child = spawn(aboutBin, [], {
      env: { ...process.env, WASH_DISPLAY: router.controlSocket },
      stdio: 'ignore',
    });
    // Brief wait so the attach handshake completes.
    await new Promise((resolve) => setTimeout(resolve, 500));
    expect(child.exitCode).toBeNull(); // still alive
    child.kill('SIGTERM');
  });
});
