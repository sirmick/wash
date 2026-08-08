// Full-stack reconnect + disconnect-diagnostics contract (docs/RECONNECT.md).
//
// The Conn unit tests (web/shell/src/ws.test.ts) and the router heartbeat
// tests (internal/router/heartbeat_test.go) cover the state machine and the
// watchdog decision in isolation. This spec drives the real thing end to
// end: a live shell, a router that dies under it, the connection banner that
// appears with its diagnostics + "Reconnect now" button, and recovery when a
// replacement router comes up on the same port (SO_REUSEADDR, http.go).
//
// We kill the router rather than simulate the slow heartbeat-zombie path: a
// SIGKILL gives an immediate socket close (so the banner appears fast and the
// 25s test budget is comfortable), while still exercising the FE reconnect
// loop, the banner UI, the retry button, and the BE-side connect log.

import { test, expect, startRouter, stopRouter, type RouterHandle } from '../fixtures/router';

const APPS = ['session', 'about', 'test'] as const;

test.use({ routerOpts: { apps: [...APPS] } });

test.describe('websocket reconnect + banner', () => {
  test('router drops → banner + Reconnect-now → same-port restart recovers', async ({ page, router }) => {
    test.setTimeout(30_000);
    const port = Number(new URL(router.url).port);
    let r2: RouterHandle | null = null;
    const banner = page.locator('[data-testid="wash-connection-banner"]');
    try {
      await page.goto(router.url);
      await expect(page.locator('wash-app-session')).toBeVisible();
      // Healthy connection: no banner.
      await expect(banner).toHaveCount(0);

      // Drop the connection: SIGKILL the router for an immediate socket close.
      router.proc.kill('SIGKILL');
      await new Promise<void>((res) => router.proc.once('exit', () => res()));

      // The shell notices and surfaces the banner with a recoverable state
      // and the diagnostics affordances.
      await expect(banner).toBeVisible({ timeout: 10_000 });
      const state = await banner.getAttribute('data-state');
      expect(['reconnecting', 'closed']).toContain(state);
      const retry = page.locator('[data-testid="wash-connection-retry"]');
      await expect(retry).toBeVisible();

      // Visible is not enough — it has to be REACHABLE. A trial click runs the
      // whole actionability chain (including hit-target) without clicking, and
      // is the regression guard for the boot splash: #wash-boot is a
      // full-screen opaque overlay torn down by wash:desktop-painted, so a drop
      // that happens BEFORE the desktop paints used to leave it sitting on top
      // of this banner for its whole 12s backstop — button visible, enabled,
      // and completely un-clickable (main.tsx, connState 'reconnecting').
      // Asserted while the port is still dead, so nothing can race it.
      await retry.click({ trial: true, timeout: 5_000 });

      // Bring a replacement router up on the SAME port the browser is
      // reconnecting to (relies on SO_REUSEADDR for the immediate rebind).
      r2 = await startRouter({ apps: [...APPS], port });

      // Skip the backoff: the "Reconnect now" button re-dials immediately.
      try {
        await retry.click({ timeout: 5_000 });
      } catch {
        // Now that the port answers again the backoff loop (250ms→4s, ws.ts)
        // can beat us to it and detach the button mid-click. That is the
        // recovery, not a failure — but only if the banner really did go:
        // otherwise the button genuinely refused the click.
        await expect(banner).toHaveCount(0);
      }

      // Recovered: banner gone, desktop back. (.first() — a reconnect can
      // briefly leave the pre-drop session element in the DOM beside the
      // freshly-declared one.)
      await expect(banner).toHaveCount(0, { timeout: 15_000 });
      await expect(page.locator('wash-app-session').first()).toBeVisible();

      // BE-side proof the FE actually reconnected to the replacement router.
      await r2.waitForLog(/shell: connect conn=/, 10_000);
    } finally {
      if (r2) await stopRouter(r2);
    }
  });
});
