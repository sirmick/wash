// Remote-apps R2 smoke (docs/REMOTE.md): a second router's app composites
// into the local desktop as a host-striped window.
//
// Two routers stand in for "host A" (the desktop) and "host B" (the remote
// host an ssh -L tunnel would reach). The browser loads A's shell and, via
// ?peer=<origin>@<ws-url>, opens a second RouterClient to B. We launch an
// app ON B over its control socket; B declares the window to the tunnelled
// shell, which mounts it tagged origin=remoteB with the per-host stripe.
//
// B runs --allow-cross-origin (the page's Origin is A, B's Host differs) —
// the loopback/SSH boundary is the real gate, not same-origin.

import { test, expect } from '@playwright/test';
import { startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

test('a remote router\'s app composites as a host-striped window', async ({ page }) => {
  let a: RouterHandle | undefined;
  let b: RouterHandle | undefined;
  try {
    a = await startRouter({ apps: ['session', 'about', 'test'] });
    b = await startRouter({ apps: ['about'], extraArgs: ['--no-session', '--allow-cross-origin'] });

    const bPort = new URL(b.url).port;
    const peer = `remoteB@ws://127.0.0.1:${bPort}/ws`;
    await page.goto(`${a.url}?peer=${encodeURIComponent(peer)}`);

    // Local desktop is up (the page loaded and its own router is serving).
    await expect(page.locator('[data-testid="wash-cam"]')).toBeAttached({ timeout: 10_000 });

    // Launch an app ON B. B declares the window + bundle to the tunnelled
    // shell (A's second RouterClient).
    const launched = await b.controlRequest({ t: 'launch', app_id: 'com.wash.about' });
    expect(launched.t).toBe('launched');

    // The window appears in A's desktop, tagged to remoteB with its stripe.
    // The stripe renders only once the Win lands in the store, which is
    // gated on B's bundle importing — so this also proves per-origin bundle
    // load + element mount over the second connection.
    const stripe = page.locator('[data-testid="wash-host-stripe"][data-origin="remoteB"]');
    await expect(stripe).toBeVisible({ timeout: 15_000 });

    // And the element instance mounted (per-origin mangled tag).
    await expect(page.locator('wash-app-about-remoteb')).toBeAttached({ timeout: 5_000 });
  } finally {
    if (a) await stopRouter(a);
    if (b) await stopRouter(b);
  }
});
