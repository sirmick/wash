// Find in scrollback (docs/Review-findings.md P2 → term "find in
// scrollback"): Ctrl+Shift+F / Alt+F opens a find bar over the focused
// pane; typing searches incrementally through @xterm/addon-search, Enter /
// Shift+Enter step, Esc closes and drops the highlights.
//
// The claim is two-sided: the VIEWPORT moved to the hit (buffer.active
// .viewportY left the bottom of a 200-line scrollback) and the hit is
// decorated (an .xterm-decoration element exists in the pane) — a search
// that only moved the selection would be invisible in a terminal.

import { test, expect } from '../fixtures/router';
import type { Locator, Page } from '@playwright/test';

async function bufferOf(host: Locator): Promise<string> {
  return await host.evaluate((el: any) => {
    const term = el.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (line) out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

async function viewportY(host: Locator): Promise<number> {
  return await host.evaluate((el: any) => el.__washTerm?.buffer.active.viewportY ?? -1);
}
async function baseY(host: Locator): Promise<number> {
  return await host.evaluate((el: any) => el.__washTerm?.buffer.active.baseY ?? -1);
}
async function decorationCount(host: Locator): Promise<number> {
  return await host.locator('.xterm-decoration').count();
}

async function openTerminal(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  await host.click();
  return host;
}

// fillScrollback prints 200 numbered lines and waits for the last one, so
// the search has history to scroll back through.
async function fillScrollback(page: Page, host: Locator) {
  await page.keyboard.type('seq 1 200; echo SEQDONE');
  await page.keyboard.press('Enter');
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/^SEQDONE$/m);
  // The viewport sits at the bottom after the burst.
  await expect.poll(async () => (await viewportY(host)) === (await baseY(host)), { timeout: 5_000 }).toBe(true);
}

test.describe('term find', () => {
  test.setTimeout(60_000);

  test('Ctrl+Shift+F finds a line deep in scrollback: viewport scrolls and the hit is decorated', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await fillScrollback(page, host);
    const bottom = await viewportY(host);
    expect(bottom).toBeGreaterThan(0);

    await host.click();
    await page.keyboard.press('Control+Shift+F');
    const bar = page.locator('[data-testid="term-find"]');
    await expect(bar).toBeVisible();
    const input = page.locator('[data-testid="term-find-input"]');
    await expect(input).toBeFocused();
    await input.fill('150');

    // Scrolled up to the hit…
    await expect.poll(() => viewportY(host), { timeout: 5_000 }).toBeLessThan(bottom);
    // …and the hit is highlighted, not just selected.
    await expect.poll(() => decorationCount(host), { timeout: 5_000 }).toBeGreaterThan(0);
    // "150" occurs once in 1..200; the bar says so.
    await expect(page.locator('[data-testid="term-find-count"]')).toHaveText('1 of 1');

    // Esc closes the bar, drops the highlights, and hands focus back.
    await input.press('Escape');
    await expect(bar).toHaveCount(0);
    await expect.poll(() => decorationCount(host), { timeout: 5_000 }).toBe(0);
    await expect(host.locator('.xterm-helper-textarea')).toBeFocused();
  });

  test('Alt+F opens it too; next/prev walk the matches; regex and case toggles apply', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await fillScrollback(page, host);

    await host.click();
    await page.keyboard.press('Alt+F');
    const input = page.locator('[data-testid="term-find-input"]');
    await expect(input).toBeFocused();
    // "19" matches 19 and 190..199 — eleven hits.
    await input.fill('19');
    const count = page.locator('[data-testid="term-find-count"]');
    await expect(count).toHaveText(/of 11$/);
    await input.press('Enter');
    await expect(count).toHaveText('2 of 11');
    await page.locator('[data-testid="term-find-prev"]').click();
    await expect(count).toHaveText('1 of 11');

    // Regex: 1[05]0 is 100 and 150 — two lines, and nothing without regex.
    await page.locator('[data-testid="term-find-regex"]').check();
    await input.fill('1[05]0');
    await expect(count).toHaveText(/of 2$/);
    await page.locator('[data-testid="term-find-regex"]').uncheck();
    await expect(count).toHaveText('no matches');

    // Case: print a mixed-case pair. The typed command line carries both
    // spellings too, so case-insensitive finds four and case-sensitive two.
    await input.press('Escape');
    await host.click();
    await page.keyboard.type("printf 'Mixed\\nmixed\\n'");
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(host), { timeout: 5_000 }).toMatch(/^mixed$/m);
    await page.keyboard.press('Alt+F');
    await input.fill('mixed');
    await expect(count).toHaveText(/of 4$/);
    await page.locator('[data-testid="term-find-case"]').check();
    await expect(count).toHaveText(/of 2$/);
    await page.locator('[data-testid="term-find-close"]').click();
    await expect(page.locator('[data-testid="term-find"]')).toHaveCount(0);
  });

  test('the Edit menu has Find…', async ({ page, router }) => {
    await openTerminal(page, router.url);
    await page.locator('[data-testid="term-menu-edit-btn"]').click();
    await page.locator('[data-testid="term-menu-find"]').click();
    await expect(page.locator('[data-testid="term-find-input"]')).toBeFocused();
  });
});
