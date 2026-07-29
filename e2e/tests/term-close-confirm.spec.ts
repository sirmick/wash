// wash-term close confirmation (issue #19): clicking the titlebar ✕ on a
// terminal with live shells must NOT close the window outright — the BE
// vetoes the close handshake and the FE shows a confirm dialog. Cancel
// keeps the window (and the shell alive); confirm kills the shells and
// the window goes away via the app-initiated confirm_close path.
//
// Also covers the app-initiated close on its own: typing `exit` in the
// last tab closes the window with no dialog (the BE's empty-session
// path sends an unsolicited ConfirmClose(true), which the router now
// honours as "app asks to close itself").

import { test, expect } from '../fixtures/router';

test.describe('terminal close confirmation', () => {
  test.setTimeout(30_000);

  test('titlebar close asks, cancel keeps, confirm closes', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();

    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    const host = page.locator('[data-testid="term-host"]').first();
    await expect(host).toBeVisible();

    // The window hosting the terminal.
    const win = page.locator('.wash-window', { has: term });
    const closeBtn = win.locator('[data-testid="window-close"]');

    // ✕ → dialog appears, window still up.
    await closeBtn.click();
    const dialog = page.locator('[data-testid="term-close-confirm"]');
    await expect(dialog).toBeVisible();
    await expect(term).toBeVisible();

    // Cancel → dialog gone, window and shell still alive.
    await page.locator('[data-testid="term-close-confirm-no"]').click();
    await expect(dialog).not.toBeVisible();
    await expect(term).toBeVisible();

    // ✕ again → confirm → window tears down.
    await closeBtn.click();
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="term-close-confirm-yes"]').click();
    await expect(term).not.toBeVisible({ timeout: 8_000 });
  });

  test('exit in the last tab closes the window without a dialog', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();

    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    const host = page.locator('[data-testid="term-host"]').first();
    await expect(host).toBeVisible();

    // Wait for a prompt so the shell is ready to take input.
    await expect
      .poll(
        () =>
          host.evaluate((h: any) => {
            const t = h.__washTerm;
            if (!t) return '';
            const buf = t.buffer.active;
            let out = '';
            for (let y = 0; y < buf.length; y++) {
              const line = buf.getLine(y);
              if (line) out += line.translateToString(true) + '\n';
            }
            return out;
          }),
        { timeout: 8_000 },
      )
      .toMatch(/[$#%>][ ]?/);

    await host.click();
    await page.keyboard.type('exit');
    await page.keyboard.press('Enter');

    // The pty dies → BE's last-session path asks the router to close →
    // window disappears, no dialog involved.
    await expect(term).not.toBeVisible({ timeout: 8_000 });
    await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
  });
});
