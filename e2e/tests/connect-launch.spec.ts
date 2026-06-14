// wash-connect launch path (docs/REMOTE.md §6.1).
//
// Proves the shell-level remote-apps primitives wash-connect drives:
// attachRemote → catalogFor → launchOn → (window composites) → detachRemote.
// Two routers stand in for the desktop (A) and the remote host (B); B runs
// --no-session --allow-cross-origin, exactly as the com.wash.remote
// supervisor brings it up over ssh -L. We drive window.wash directly here
// because the supervisor's SSH leg + the wash-connect window UI need a real
// second host — that's the capstone two-VM Playwright test (deferred to M2e).
//
// This is the CI-runnable proof of the M3 plumbing: per-origin catalog,
// the shell.launch ctrl verb, and attach/detach + dropOrigin.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

test('attach → list B\'s catalog → launchOn → composite → detach', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: ['session', 'about', 'connect', 'remote', 'test'] });
    b = await startRouter({ apps: ['about'], extraArgs: ['--no-session', '--allow-cross-origin'] });
    const bURL = `ws://127.0.0.1:${new URL(b.url).port}/ws`;

    await page.goto(a.url);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // Attach B as a second RouterClient — what wash-connect does once the
    // supervisor reports the host "up" with its local endpoint.
    await page.evaluate((url) => window.wash.attachRemote('remoteB', url), bURL);

    // B's catalog arrives on connect (per-origin un-guard, M3 plumbing #1).
    await expect
      .poll(async () => page.evaluate(() => window.wash.catalogFor('remoteB').map((x) => x.id)), { timeout: 10_000 })
      .toContain('com.wash.about');

    // launchOn → shell.launch ctrl verb on B's RouterClient (plumbing #2);
    // B spawns the app and declares it back over the tunnelled connection.
    await page.evaluate(() => window.wash.launchOn('remoteB', 'com.wash.about'));

    // The window composites into A's desktop, host-striped + per-origin tag.
    const stripe = page.locator('[data-testid="wash-host-stripe"][data-origin="remoteB"]');
    await expect(stripe).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('wash-app-about-remoteb')).toBeAttached({ timeout: 5_000 });

    // detachRemote drops the host's windows + clears its catalog (plumbing #3).
    await page.evaluate(() => window.wash.detachRemote('remoteB'));
    await expect(stripe).toHaveCount(0, { timeout: 10_000 });
    expect(await page.evaluate(() => window.wash.catalogFor('remoteB'))).toEqual([]);
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});

test('wash-connect window renders the connect form', async ({ page }) => {
  let a: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: ['session', 'connect', 'remote'] });
    await page.goto(a.url);
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // Launch the connect window locally (control socket allows window apps).
    const launched = await a.controlRequest({ t: 'launch', app_id: 'com.wash.connect' });
    expect(launched.t).toBe('launched');

    // The app bundle loads, the element mounts, and its connect form shows —
    // with no hosts yet, the empty-state hint is present.
    await expect(page.locator('[data-testid="connect-host-input"]')).toBeVisible({ timeout: 15_000 });
    await expect(page.locator('[data-testid="connect-submit"]')).toBeVisible();
  } finally {
    if (a) await stopRouter(a);
  }
});
