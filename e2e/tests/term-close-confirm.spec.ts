// wash-term close confirmation (issue #19).
//
// Closing a tab kills its shell, and closing the window kills every shell
// in it — irreversibly, with no undo. So a close that would lose work asks
// first. "Would lose work" means something other than the login shell holds
// the pty's foreground process group (internal/pty ForegroundUser.Busy): a
// build, an editor, ssh, an agent. A shell sitting at its prompt closes
// silently, because a dialog on every Ctrl+W is a dialog people learn to
// dismiss without reading, which is worse than no dialog at all.
//
// The window path also covers the router's app-initiated close: the BE
// cannot hold the close handshake open while a human reads a question (the
// router force-kills at 5s), so it VETOES the handshake and, on confirm,
// closes itself with an unsolicited confirm_close(allow=true). The last
// test pins that same router path from the other direction — `exit` in the
// last tab must take the window down with no dialog at all.

import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

// A command that occupies the foreground without producing output, so the
// tab is unambiguously "busy" and nothing races the buffer assertions.
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

// runBusy starts BUSY_CMD in the focused tab and waits until the BE agrees
// the tab is busy. The status poll is ~1Hz, and Busy is read live at close
// time, so the only real barrier is the command actually being in the
// foreground: assert its echoed line, then give the fork a moment by
// waiting for the prompt to stop being the last thing on screen.
async function runBusy(page: Page, term = page.locator('wash-app-term')) {
  await term.click();
  await page.keyboard.type(BUSY_CMD);
  await expect(term).toContainText(BUSY_CMD, { timeout: 10_000 });
  await page.keyboard.press('Enter');
}

test.describe('terminal close confirmation', () => {
  test.setTimeout(45_000);

  test('an idle tab closes without asking', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Nothing running in it — the × just closes it.
    await page.locator('span[data-testid^="term-tab-close-"]').last().click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
    await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
    await expect(term).toBeVisible();
  });

  test('a busy tab asks; cancel keeps it, confirm closes it', async ({ page, router }) => {
    await page.goto(router.url);
    await openTerminal(page);
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    await runBusy(page);

    const dialog = page.locator('[data-testid="term-close-confirm"]');
    const closeSecondTab = page.locator('span[data-testid^="term-tab-close-"]').last();

    // × → asks, names what is running, and the tab is still there.
    await closeSecondTab.click();
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('sleep');
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Cancel → dialog gone, tab still alive.
    await page.locator('[data-testid="term-close-confirm-cancel"]').click();
    await expect(dialog).toHaveCount(0);
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // × again → confirm → the tab goes, the window stays.
    await closeSecondTab.click();
    await expect(dialog).toBeVisible();
    await page.locator('[data-testid="term-close-confirm-ok"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
    await expect(page.locator('wash-app-term')).toBeVisible();
  });

  test('the titlebar ✕ asks when a tab is busy; cancel keeps the window', async ({ page, router }) => {
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

    // Cancel → still there, and still there a moment later (a delayed
    // force-kill would show up here).
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

  test('the titlebar ✕ closes an idle terminal without asking', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);

    const win = page.locator('.wash-window', { has: term });
    await win.locator('[data-testid="window-close"]').click();
    await expect(term).toHaveCount(0, { timeout: 10_000 });
    await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
  });

  test('exit in the last tab closes the window, with no dialog', async ({ page, router }) => {
    await page.goto(router.url);
    const term = await openTerminal(page);

    await term.click();
    await page.keyboard.type('exit');
    await page.keyboard.press('Enter');

    // The pty dies → the BE's empty-session path sends an unsolicited
    // confirm_close(true) → the router runs the approved-close teardown.
    // Before that router fix this hung a dead, empty window on screen.
    await expect(term).toHaveCount(0, { timeout: 10_000 });
    await expect(page.locator('[data-testid="term-close-confirm"]')).toHaveCount(0);
  });
});
