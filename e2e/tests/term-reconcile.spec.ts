// Terminal stale-tab reconcile: a pty that exits while the browser is
// detached must not come back as a dead tab.
//
// The BE's `tab_closed` app_msg is dropped when no shell is attached
// (the router only buffers raw channels for replay), and the saved
// tab list is FE-written — so without the mount-time list_sessions
// reconcile, the restored FE re-adds the dead tab and the user types
// into a closed channel.
//
// Sequence:
//   1. Open wash-term, open a second tab.
//   2. In the second tab, `exec sleep 1` — the shell process is
//      replaced, so when sleep exits the pty session closes.
//   3. Navigate away (detach) BEFORE the exit lands, wait it out,
//      navigate back.
//   4. The restored window must show exactly one live tab; the dead
//      tab was dropped by the list_sessions reconcile.

import { test, expect } from '../fixtures/router';

test.describe('terminal reconcile', () => {
  test('pty that exits while detached does not come back as a dead tab', async ({ page, router }) => {
    test.setTimeout(30_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

    // Second tab.
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid^="term-tab-"]:not([data-testid*="close"])')).toHaveCount(2, {
      timeout: 10_000,
    });
    // The new tab is auto-activated; wait for its prompt then replace
    // its shell with a short-lived sleep.
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
    await term.click();
    await page.keyboard.type('exec sleep 1');
    await page.keyboard.press('Enter');

    // Detach before the sleep exits; the tab_closed fired during
    // detach goes nowhere.
    await page.goto('about:blank');
    await page.waitForTimeout(2_500);

    // Reattach. The saved state still lists both tabs; the
    // list_sessions reconcile must drop the dead one.
    await page.goto(router.url);
    const term2 = page.locator('wash-app-term');
    await expect(term2).toBeVisible();
    await expect(page.locator('[data-testid^="term-tab-"]:not([data-testid*="close"])')).toHaveCount(1, {
      timeout: 10_000,
    });

    // The surviving tab is live: typing echoes back.
    const marker = `wash-reconcile-${Date.now()}`;
    await term2.click();
    await page.keyboard.type(`echo ${marker}`);
    await page.keyboard.press('Enter');
    await expect(term2).toContainText(marker, { timeout: 5_000 });
  });
});
