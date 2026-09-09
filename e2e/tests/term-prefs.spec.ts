// Desktop-wide terminal preferences and zoom (docs/Review-findings.md
// P2 → term "preferences are per-window … no Ctrl+±/wheel zoom"):
//
//   - font, palette, smart-paste mode, cursor and scrollback live in the
//     BE's ~/.config/wash/term.json, not in the window's blob, so a SECOND
//     terminal window opens with what you chose in the first;
//   - a change in one open window reaches the other one live, because the
//     file is the only channel two wash-term processes share;
//   - Ctrl+= / Ctrl+- / Ctrl+0 and Ctrl+wheel zoom the terminal font (not
//     the page) and the size sticks like any other font change.

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

async function launchTerminal(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
}

async function openTerminal(page: Page, url: string): Promise<Locator> {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await launchTerminal(page);
  await expect(page.locator('wash-app-term')).toBeVisible();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  await expect.poll(() => bufferOf(host), { timeout: 10_000 }).toMatch(/[$#%>][ ]?/);
  await host.click();
  return host;
}

// fontSizeOf reads the live xterm's size, which is what the preference
// actually has to change — a menu that only updated its own label would
// pass any test that read the label.
const fontSizeOf = (host: Locator) =>
  host.evaluate((el: any) => el.__washTerm?.options.fontSize ?? 0);

test.describe('term prefs', () => {
  test.setTimeout(90_000);

  test('a font size set in one window is what the NEXT window opens with', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const start = await fontSizeOf(host);

    await page.locator('[data-testid="term-menu-font-btn"]').click();
    await page.locator('[data-testid="term-menu-size-inc"]').click();
    await page.locator('[data-testid="term-menu-size-inc"]').click();
    await page.keyboard.press('Escape');
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start + 2);

    // A second window: a NEW wash-term process, which learns the size from
    // the prefs file rather than from anything this window told it.
    await launchTerminal(page);
    await expect.poll(async () => await page.locator('wash-app-term').count(), { timeout: 20_000 }).toBe(2);
    const second = page.locator('wash-app-term').nth(1).locator('[data-testid="term-host"]').first();
    await expect(second).toBeVisible();
    await expect.poll(() => fontSizeOf(second), { timeout: 20_000 }).toBe(start + 2);
  });

  test('a change in one window reaches the other one live', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    await launchTerminal(page);
    await expect.poll(async () => await page.locator('wash-app-term').count(), { timeout: 20_000 }).toBe(2);
    const second = page.locator('wash-app-term').nth(1).locator('[data-testid="term-host"]').first();
    await expect(second).toBeVisible();
    await expect.poll(() => fontSizeOf(second), { timeout: 20_000 }).toBeGreaterThan(0);
    const start = await fontSizeOf(host);
    expect(await fontSizeOf(second)).toBe(start);

    // Change it in the SECOND window (the one on top, and so the one that
    // can be clicked) and watch the FIRST follow — it is never touched,
    // and the two are separate wash-term processes.
    await page.locator('wash-app-term').nth(1).locator('[data-testid="term-menu-font-btn"]').click();
    // The menu itself renders at document level, not inside the app
    // element, so it is addressed unscoped — only one is ever open.
    await page.locator('[data-testid="term-menu-size-inc"]').click();
    await page.keyboard.press('Escape');

    await expect.poll(() => fontSizeOf(host), { timeout: 20_000 }).toBe(start + 1);
  });

  test('Ctrl+= / Ctrl+- / Ctrl+0 zoom the terminal font', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const start = await fontSizeOf(host);

    await host.click();
    await page.keyboard.press('Control+Equal');
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start + 1);
    await page.keyboard.press('Control+Equal');
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start + 2);
    await page.keyboard.press('Control+Minus');
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start + 1);
    // Ctrl+0 goes back to the default, whatever the size had drifted to.
    await page.keyboard.press('Control+0');
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start);
  });

  test('Ctrl+wheel zooms too', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const start = await fontSizeOf(host);
    const box = await host.boundingBox();
    await page.mouse.move(box!.x + box!.width / 2, box!.y + box!.height / 2);
    await page.keyboard.down('Control');
    await page.mouse.wheel(0, -120);
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start + 1);
    await page.mouse.wheel(0, 120);
    await expect.poll(() => fontSizeOf(host), { timeout: 5_000 }).toBe(start);
    await page.keyboard.up('Control');
  });

  test('scrollback is a preference, applied to the live terminal', async ({ page, router }) => {
    const host = await openTerminal(page, router.url);
    const scrollbackOf = () => host.evaluate((el: any) => el.__washTerm?.options.scrollback ?? 0);
    const start = await scrollbackOf();
    expect(start).toBeGreaterThan(0);

    await page.locator('[data-testid="term-menu-font-btn"]').click();
    await page.locator('[data-testid="term-menu-scroll-dec"]').click();
    await page.keyboard.press('Escape');
    await expect.poll(scrollbackOf, { timeout: 5_000 }).toBe(start / 2);

    await launchTerminal(page);
    await expect.poll(async () => await page.locator('wash-app-term').count(), { timeout: 20_000 }).toBe(2);
    const second = page.locator('wash-app-term').nth(1).locator('[data-testid="term-host"]').first();
    await expect(second).toBeVisible();
    await expect.poll(
      () => second.evaluate((el: any) => el.__washTerm?.options.scrollback ?? 0),
      { timeout: 20_000 },
    ).toBe(start / 2);
  });
});
