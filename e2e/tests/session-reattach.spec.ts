// Session foundation: the router owns WM state. A browser refresh
// re-subscribes via session.snapshot and the open windows come back
// where they were, with the same titles, geometry, and state.

import { test, expect } from '../fixtures/router';

test.describe('session reattach', () => {
  test.use({ routerOpts: { showHidden: true } });

  test('window geometry survives a browser refresh', async ({ page, router }) => {
    await page.goto(router.url);

    // Open wash-test.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /wash test/ }).click();
    const app = page.locator('wash-app-test');
    await expect(app).toBeVisible();

    // Drag the window via the titlebar to a known position. We move
    // it 100 px right + 50 px down from its starting cascade slot.
    const frame = page.locator('.wash-window').first();
    const titlebar = frame.locator('.wash-titlebar');
    const tbBox = await titlebar.boundingBox();
    expect(tbBox).not.toBeNull();
    const startX = tbBox!.x + 30;
    const startY = tbBox!.y + 6;
    await page.mouse.move(startX, startY);
    await page.mouse.down();
    await page.mouse.move(startX + 100, startY + 50, { steps: 4 });
    await page.mouse.up();

    // The drag commits on pointer-up via window.move; the router
    // applies it and broadcasts the patch. Read back the frame's
    // current position so we can compare across refresh.
    await expect.poll(async () => {
      const b = await frame.boundingBox();
      return b ? Math.round(b.x) : null;
    }).toBeGreaterThan(Math.round(tbBox!.x) + 90);
    const beforeBox = await frame.boundingBox();
    expect(beforeBox).not.toBeNull();

    // Refresh the browser. The router keeps the app process alive
    // and the window in winSession; on reconnect, the snapshot
    // restores the frame in place.
    await page.reload();
    await expect(page.locator('wash-app-test')).toBeVisible();

    // After the refresh the FE has been wiped, but session.snapshot
    // brings the window back at the same position (within a couple
    // px for any rounding).
    const afterFrame = page.locator('.wash-window').first();
    const afterBox = await afterFrame.boundingBox();
    expect(afterBox).not.toBeNull();
    expect(Math.abs(afterBox!.x - beforeBox!.x)).toBeLessThan(3);
    expect(Math.abs(afterBox!.y - beforeBox!.y)).toBeLessThan(3);
    expect(Math.abs(afterBox!.width - beforeBox!.width)).toBeLessThan(3);
    expect(Math.abs(afterBox!.height - beforeBox!.height)).toBeLessThan(3);
  });

  test('multiple windows + minimized state survive a refresh', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /wash test/ }).click();
    await expect(page.locator('wash-app-test')).toHaveCount(1);

    // Spawn a second wash-test (cascades to a new offset).
    await page.locator('wash-app-test').first().locator('[data-testid="action-spawn-self"]').click();
    await expect(page.locator('wash-app-test')).toHaveCount(2);

    // Minimize the first window via its titlebar minimize button.
    await page.locator('.wash-window').first().locator('[data-testid="window-minimize"]').click();
    // Minimized → display:none — taskbar pill is the only handle.
    await expect(page.locator('.wash-window').first()).toBeHidden();

    await page.reload();

    // Two test windows again; exactly one minimized, one visible.
    // DOM order from the snapshot is undefined, so we count rather
    // than indexing.
    await expect(page.locator('wash-app-test')).toHaveCount(2);
    await expect(page.locator('.wash-window:visible')).toHaveCount(1);
    // The hidden one has display:none (minimized state).
    const hiddenCount = await page.locator('.wash-window').evaluateAll(
      (els) => els.filter((e) => (e as HTMLElement).style.display === 'none').length,
    );
    expect(hiddenCount).toBe(1);
  });
});
