// Reconnect TORTURE — the browser drops, repeatedly, while traffic is in
// flight (GH #22, #23).
//
// reconnect.spec.ts covers the honest single drop: kill the router, watch
// the banner, come back. This covers the case that actually broke on a
// real box — the connection going away WHILE apps are mid-write, over and
// over, with the router alive the whole time.
//
// The mechanism from #23's log:
//
//   app com.wash.session loop instance=i-8: router: qos scheduler closed
//   app com.wash.session crashed: code=1 uptime=48m4s instance=i-8
//   ...
//   app com.wash.session up instance=i-16
//
// A transport write fails, drainLoop closes the QoS scheduler, and every
// producer blocked in Submit unblocks with ErrSchedulerClosed. That error
// returns straight up through dispatch, and AppInstance.loop is just
// ReadLoop — ANY dispatch error ends it. So an app that happened to be
// writing to the shell when the browser went away is torn down by it, and
// the reconnect brings up a NEW instance with none of the old state.
//
// That is backwards from the design: the reattach/replay machinery exists
// precisely so BE apps outlive a browser. The raw path already knows it
// (a nil shell drops to the ring buffer instead of erroring); the
// lossless and control paths treat "browser gone" as fatal.
//
// So the assertions here are about SURVIVAL, not about pixels:
//   - no app crashes in the router log
//   - the desktop instance id is the SAME one after the drops
//   - windows keep their titlebar icons (#22)
//
// The lever is window.__washDropSocket() — a real WS drop with no page
// reload, the same one term-live-reconnect uses. Killing the router would
// prove nothing: apps SHOULD die with their router.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect, type RouterHandle } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify', 'term', 'fm'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// Repeated cold drops plus a router boot blow well past the default.
test.describe.configure({ timeout: 120_000 });

/** Every "app <id> up instance=<i-N>" the router has logged, in order. */
function instancesUp(log: string, appID: string): string[] {
  const re = new RegExp(`app ${appID.replace(/\./g, '\\.')} up instance=(\\S+)`, 'g');
  return [...log.matchAll(re)].map((m) => m[1]);
}

/** Crash lines are the failure this whole spec exists to catch. */
function crashes(log: string): string[] {
  return log.split('\n').filter((l) => / crashed: code=/.test(l));
}

/**
 * One real transport drop, and the wait for the shell to come back.
 *
 * Waiting on totalReconnects rather than a timeout: the point of the
 * torture is many drops, and a sleep long enough to be safe would make
 * the spec minutes long, while one too short would silently test fewer
 * drops than it claims.
 */
async function dropAndRecover(page: Page) {
  const before = await page.evaluate(
    () => (window as unknown as { __washDiag?: () => { conn: { totalReconnects: number } } }).__washDiag?.().conn.totalReconnects ?? 0,
  );
  await page.evaluate(() => (window as unknown as { __washDropSocket?: () => void }).__washDropSocket?.());
  await expect
    .poll(
      async () =>
        page.evaluate(
          () => (window as unknown as { __washDiag?: () => { conn: { totalReconnects: number } } }).__washDiag?.().conn.totalReconnects ?? 0,
        ),
      { timeout: 20_000 },
    )
    .toBeGreaterThan(before);
}

async function openDesktop(page: Page, router: RouterHandle) {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
}

async function launch(page: Page, name: string, element: string) {
  await page.locator('button[title="Apps"]').click();
  // exact: the start menu has more than one entry beginning "Terminal".
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name, exact: true })
    .first()
    .click();
  await expect(page.locator(element).first()).toBeVisible({ timeout: 20_000 });
}

test.describe('reconnect torture', () => {
  // The headline case. Traffic is flowing (an agent streaming its reply,
  // the desktop's own host-stats ticker) when the socket goes — which is
  // exactly the window in which Submit is blocked and the error becomes
  // fatal.
  test('repeated drops mid-traffic never kill a backend app', async ({ page, router }) => {
    await openDesktop(page, router);
    await launch(page, 'Terminal', 'wash-app-term');
    await launch(page, 'Files', 'wash-app-fm');

    const deskBefore = instancesUp(router.log(), 'com.wash.session');
    expect(deskBefore.length).toBe(1);

    // Keep real traffic in flight across the drops: a terminal writing
    // continuously (raw channel) and an agent streaming (app_msg events
    // on the control/lossless path, which is the one that kills).
    // The terminal takes focus on launch, so type straight into it —
    // clicking the custom element itself just hits a non-interactive box.
    await page.keyboard.type('while true; do date; done\n');

    await launch(page, 'Agent', 'wash-app-ai');
    const ai = page.locator('wash-app-ai').first();
    await ai.locator('select').selectOption('codex');
    await ai.getByRole('button', { name: 'Start session' }).click();
    const composer = ai.locator('textarea');
    await expect(composer).toBeVisible({ timeout: 20_000 });

    for (let i = 0; i < 8; i++) {
      // Re-prompt each round so a stream is genuinely in flight when the
      // socket dies, rather than the drops landing in a quiet gap.
      await composer.fill(`round ${i}`);
      await composer.press('Enter');
      await dropAndRecover(page);
    }

    // BE half: nothing died. This is the assertion #23 is about.
    expect(crashes(router.log()).join('\n')).toBe('');

    // And specifically: the desktop is the SAME instance it started as.
    // A respawn is how the window model gets lost even when nothing
    // logs the word "crash".
    const deskAfter = instancesUp(router.log(), 'com.wash.session');
    expect(deskAfter).toEqual(deskBefore);

    // FE half: the desktop is alive and the windows are still there.
    await expect(page.locator('wash-app-session')).toBeVisible();
    await expect(page.locator('[data-testid="window-crashed"]')).toHaveCount(0);
    await expect(page.locator('wash-app-term').first()).toBeVisible();
    await expect(page.locator('wash-app-fm').first()).toBeVisible();
  });

  // GH #22. Icons are manifest-derived and ride the window record, so a
  // window that comes back without one means the record was rebuilt from
  // something thinner than the manifest.
  test('windows keep their titlebar icons across repeated drops', async ({ page, router }) => {
    await openDesktop(page, router);
    await launch(page, 'Terminal', 'wash-app-term');
    await launch(page, 'Files', 'wash-app-fm');

    const icons = page.locator('[data-testid="window-icon"]');
    await expect(icons).not.toHaveCount(0);
    const before = await icons.evaluateAll((els) =>
      els.map((e) => e.getAttribute('data-icon')).sort(),
    );
    // Guard the guard: an empty list would make the comparison below
    // vacuously true.
    expect(before.filter(Boolean).length).toBe(before.length);

    for (let i = 0; i < 6; i++) await dropAndRecover(page);

    await expect(icons).toHaveCount(before.length, { timeout: 20_000 });
    const after = await icons.evaluateAll((els) =>
      els.map((e) => e.getAttribute('data-icon')).sort(),
    );
    expect(after).toEqual(before);
  });

  // A drop during window churn: windows opening and closing is when the
  // control path (channel bind, window declare) is busiest, and control
  // writes are the ones that return ErrSchedulerClosed straight into
  // ReadLoop.
  test('drops during window churn leave the roster consistent', async ({ page, router }) => {
    await openDesktop(page, router);
    const deskBefore = instancesUp(router.log(), 'com.wash.session');

    for (let i = 0; i < 5; i++) {
      await launch(page, 'Terminal', 'wash-app-term');
      await dropAndRecover(page);
      // Close it again from the titlebar, so the next round re-binds
      // channels rather than reusing a settled window.
      const close = page.locator('wash-app-term').first().locator('..').locator('[data-testid="window-close"]');
      if (await close.count()) await close.first().click({ timeout: 5_000 }).catch(() => {});
    }

    expect(crashes(router.log()).join('\n')).toBe('');
    expect(instancesUp(router.log(), 'com.wash.session')).toEqual(deskBefore);
    await expect(page.locator('wash-app-session')).toBeVisible();
  });
});
