// The shell head across two tabs (router.go attachShell / claimHead /
// promoteHead, main.tsx claimAfterAttach / claimOnUse).
//
// One tab at a time holds the head: terminal output and display pixels go
// only to it. The head follows use — a tab opened in view takes it, and
// clicking or typing in a tab that doesn't hold it takes it back, with a thin
// band across the top of the other. A redial never takes it on its own: a
// background tab the router kept reaping for a throttled heartbeat used to
// take the terminals off the tab in use on every redial, about every 95s.
// And when the tab holding the head closes, the one left behind is promoted.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

const band = (p: Page) => p.locator('[data-testid="wash-connection-superseded"]');
const reconnects = (p: Page) => p.evaluate(() => (window as any).__washDiag?.().conn.totalReconnects ?? 0);

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

// redial drops p's socket and waits for it to come back.
async function redial(p: Page): Promise<void> {
  const before = await reconnects(p);
  await p.evaluate(() => (window as any).__washDropSocket());
  await expect.poll(() => reconnects(p), { timeout: 15_000 }).toBeGreaterThan(before);
}

test.describe('use here', () => {
  test('the head follows use, redials keep it where it is, and the survivor is promoted', async ({ page, router, context }) => {
    test.setTimeout(90_000);
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });

    // A second tab opened in view takes the head; the first shows the band.
    const second = await context.newPage();
    await second.goto(router.url);
    await expect(second.locator('wash-app-term')).toBeVisible({ timeout: 15_000 });
    await expect(band(page)).toBeVisible({ timeout: 10_000 });
    await expect(band(second)).toHaveCount(0);
    await typeMarker(second, 'second');

    // Using the first tab takes the head back — no button, no reconnect —
    // and the keys that took it still reach the terminal.
    const before = await reconnects(page);
    await typeMarker(page, 'reclaimed');
    await expect(band(page)).toHaveCount(0);
    await expect(band(second)).toBeVisible({ timeout: 10_000 });
    expect(await reconnects(page)).toBe(before);

    // The tab that isn't the head redials: it does not take the head.
    await redial(second);
    await expect(band(second)).toBeVisible({ timeout: 10_000 });
    await page.waitForTimeout(1_000);
    await expect(band(page)).toHaveCount(0);

    // The head redials: it claims back the head it held.
    await redial(page);
    await page.waitForTimeout(1_000);
    await expect(band(page)).toHaveCount(0);
    await expect(band(second)).toBeVisible();

    // The head closes: the tab left behind is promoted and its terminal
    // is live without anyone clicking anything.
    await page.close();
    await expect(band(second)).toHaveCount(0, { timeout: 10_000 });
    await typeMarker(second, 'promoted');
    await second.close();
  });
});
