// The shell head across two tabs (router.go reattachChannelsToShell /
// promoteHead, main.tsx claimHead).
//
// One tab at a time holds the head: terminal output and display pixels go
// only to it. A second tab takes it, and the first shows the "in use in
// another window" banner. "Use here" takes it back on the open socket — it
// used to call reconnectNow(), a no-op while the socket is open, so the
// button did nothing. And when the tab holding the head closes, the one
// left behind is promoted rather than sitting dark until clicked.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

const banner = (p: Page) => p.locator('[data-testid="wash-connection-superseded"]');

// typeMarker runs an echo in the page's terminal and waits for its output.
// The '' split keeps the typed command line from matching the output.
async function typeMarker(p: Page, tag: string): Promise<void> {
  const term = p.locator('wash-app-term');
  const id = `${tag}-${Date.now()}`;
  await term.click();
  await p.keyboard.type(`echo wash-mark''er-${id}`);
  await p.keyboard.press('Enter');
  await expect(term).toContainText(`wash-marker-${id}`, { timeout: 10_000 });
}

test.describe('use here', () => {
  test('the head moves between tabs and back, and survives the head closing', async ({ page, router, context }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

    // A second tab takes the head; the first is told.
    const second = await context.newPage();
    await second.goto(router.url);
    await expect(second.locator('wash-app-term')).toBeVisible({ timeout: 15_000 });
    await expect(banner(page)).toBeVisible({ timeout: 10_000 });
    await expect(banner(second)).toHaveCount(0);
    await typeMarker(second, 'second');

    // "Use here" brings it back, without a reconnect, and the other tab is
    // told in turn.
    const reconnects = await page.evaluate(() => (window as any).__washDiag?.().conn.totalReconnects ?? 0);
    await page.locator('[data-testid="wash-connection-use-here"]').click();
    await expect(banner(page)).toHaveCount(0);
    await expect(banner(second)).toBeVisible({ timeout: 10_000 });
    await typeMarker(page, 'reclaimed');
    expect(await page.evaluate(() => (window as any).__washDiag?.().conn.totalReconnects ?? 0)).toBe(reconnects);

    // The head closes: the tab left behind is promoted and its terminal
    // is live without anyone clicking anything.
    await page.close();
    await expect(banner(second)).toHaveCount(0, { timeout: 10_000 });
    await typeMarker(second, 'promoted');
    await second.close();
  });
});
