// Unicode 11 widths and the cursor preference (docs/Review-findings.md
// P2 → term "unicode11 … cursor style"):
//
//   - xterm's active unicode version is '11', so an emoji occupies the two
//     cells it is drawn in. Under the default (version 6) tables it counts
//     as one, and every column after it on the line is off by one — which
//     is what makes a starship / powerlevel prompt smear;
//   - cursor shape and blink are a menu choice, applied to the live
//     terminal (no remount, so scrollback survives) and persisted with the
//     window's other appearance settings.

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

test.describe('term unicode + cursor', () => {
  test.setTimeout(60_000);

  test('the active unicode version is 11 and an emoji measures two cells', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const info = await host.evaluate((el: any) => ({
      active: el.__washTerm.unicode.activeVersion,
      versions: el.__washTerm.unicode.versions,
      // U+1F600 is width 2 under Unicode 11 and width 1 under 6.
      emoji: el.__washTerm._core?.unicodeService?.getStringCellWidth?.('\u{1F600}'),
    }));
    expect(info.versions).toContain('11');
    expect(info.active).toBe('11');
    expect(info.emoji).toBe(2);
  });

  test('the Cursor menu changes the live cursor and the choice survives a remount', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const optionsOf = () => host.evaluate((el: any) => ({
      style: el.__washTerm.options.cursorStyle,
      blink: el.__washTerm.options.cursorBlink,
    }));
    expect(await optionsOf()).toEqual({ style: 'block', blink: true });

    // Something in the scrollback, so a cursor change that silently
    // remounted the terminal would be visible as its loss.
    await host.click();
    await page.keyboard.type('echo CURSORMARK');
    await page.keyboard.press('Enter');
    await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/^CURSORMARK$/m);

    await page.locator('[data-testid="term-menu-cursor-btn"]').click();
    await page.locator('[data-testid="term-menu-cursor-bar"]').click();
    await expect.poll(async () => (await optionsOf()).style, { timeout: 5_000 }).toBe('bar');

    await page.locator('[data-testid="term-menu-cursor-btn"]').click();
    await page.locator('[data-testid="term-menu-cursor-blink"]').click();
    await expect.poll(async () => (await optionsOf()).blink, { timeout: 5_000 }).toBe(false);
    expect(await bufferOf(host)).toMatch(/^CURSORMARK$/m);

    // Persisted with the window's other appearance state: a reload
    // restores it from the router's blob.
    await page.reload();
    await expect(page.locator('wash-app-term')).toBeVisible();
    const host2 = page.locator('[data-testid="term-host"]').first();
    await expect(host2).toBeVisible();
    await expect.poll(
      () => host2.evaluate((el: any) => el.__washTerm?.options.cursorStyle ?? ''),
      { timeout: 15_000 },
    ).toBe('bar');
    expect(await host2.evaluate((el: any) => el.__washTerm.options.cursorBlink)).toBe(false);
  });
});
