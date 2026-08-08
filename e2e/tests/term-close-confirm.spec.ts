// wash-term close confirmation (issue #19).
//
// Closing a tab kills its shell, and closing the window kills every shell
// in it, with no undo — so both ask first, ALWAYS. Not only when something
// is running: a shell at a prompt still holds scrollback, a half-typed
// command, an ssh session between commands, and nothing in the FE can tell
// which of those the user cares about. The dialog names the foreground
// program per tab when there is one and says "at a prompt" when there is
// not, so the cost of confirming is on screen either way.
//
// The window path also covers the router's app-initiated close: the BE
// cannot hold the close handshake open while a human reads a question (the
// router force-kills at 5s), so it VETOES the handshake and, on confirm,
// closes itself with an unsolicited confirm_close(allow=true). The last
// test pins that same router path from the other direction — `exit` in the
// last tab must take the window down with no dialog at all, because the
// user did not ask to close anything, the shell just ended.

import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

// Occupies the foreground without producing output, so "running something"
// is unambiguous and nothing races the buffer assertions.
const BUSY_CMD = 'sleep 30';

async function openTerminal(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  // A prompt means the shell is ready for input — xterm being mounted does
  // not (docs/TEST_FLAKES.md C5).
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  return term;
}

async function runBusy(page: Page, term = page.locator('wash-app-term')) {
  await term.click();
  await page.keyboard.type(BUSY_CMD);
  await expect(term).toContainText(BUSY_CMD, { timeout: 10_000 });
  await page.keyboard.press('Enter');
}

test.describe('terminal close confirmation', () => {
  test.setTimeout(45_000);

  test('closing an idle tab still asks, and says it is at a prompt', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    const dialog = page.locator('[data-testid="term-close-confirm"]');
    const closeSecondTab = page.locator('span[data-testid^="term-tab-close-"]').last();

    await closeSecondTab.click();
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('at a prompt');
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Cancel keeps it.
    await page.locator('[data-testid="term-close-confirm-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Confirm closes it; the window and the other tab stay.
    await closeSecondTab.click();
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
    await expect(term).toBeVisible();
  });

  test('a busy tab names what it is running', async ({ page, router }) => {
    await page.goto(router.url);
    await openTerminal(page);
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    await runBusy(page);

    const dialog = page.locator('[data-testid="term-close-confirm"]');
    await page.locator('span[data-testid^="term-tab-close-"]').last().click();
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('sleep');

    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
  });

  test('the titlebar ✕ asks, and a cancelled close outlives the router grace', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);
    await runBusy(page, term);

    const win = page.locator('.wash-window', { has: term });
    const closeBtn = win.locator('[data-testid="window-close"]');
    const dialog = page.locator('[data-testid="term-close-confirm"]');

    // ✕ → the BE vetoes the handshake and the FE asks. The window must
    // survive the veto — the router's 5s grace force-kill is exactly what
    // this path exists to avoid.
    await closeBtn.click();
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('sleep');
    await expect(term).toBeVisible();

    await page.locator('[data-testid="term-close-confirm-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await page.waitForTimeout(6_000); // > the router's 5s close grace
    await expect(term).toBeVisible();

    // ✕ → confirm → the window tears down via the app-initiated close.
    await closeBtn.click();
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(term).toHaveCount(0, { timeout: 10_000 });
  });

  test('the titlebar ✕ asks even when every tab is idle', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);

    const win = page.locator('.wash-window', { has: term });
    const dialog = page.locator('[data-testid="term-close-confirm"]');

    await win.locator('[data-testid="window-close"]').click();
    await expect(dialog).toBeVisible();
    await expect(term).toBeVisible();

    await page.locator('[data-testid="term-close-confirm-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(term).toBeVisible();
  });

  test('exit in the last tab closes the window, with no dialog', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);

    await term.click();
    await page.keyboard.type('exit');
    await page.keyboard.press('Enter');

    // The pty dies → the BE's empty-session path sends an unsolicited
    // confirm_close(true) → the router runs the approved-close teardown.
    // Nothing to confirm: the user ended the shell themselves.
    await expect(term).toHaveCount(0, { timeout: 10_000 });
    await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
  });
});
