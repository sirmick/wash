// wash-term font + copy/paste UX: the menubar "Font" menu offers a
// font-size stepper and a font-family picker; right-click is the
// Copy/Paste/Paste selection menu (docs/CLIPBOARD.md). Font size and family are window-wide (persisted in
// app state) and apply live to xterm. Copy/paste go through the real
// browser clipboard so they interop with the host OS.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function openTerminal(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Terminal', exact: true })
    .click();
  const host = page.locator('[data-testid="term-host"]').first();
  await expect(host).toBeVisible();
  // Wait for the shell prompt so the PTY is live before we drive it.
  await expect
    .poll(() => bufferText(page), { timeout: 8_000 })
    .toMatch(/[$#%>][ ]?/);
  return host;
}

async function bufferText(page: Page): Promise<string> {
  return await page
    .locator('[data-testid="term-host"]')
    .first()
    .evaluate((host: any) => {
      const term = host.__washTerm;
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

function termOption(page: Page, key: string) {
  return page
    .locator('[data-testid="term-host"]')
    .first()
    .evaluate((host: any, k: string) => host.__washTerm?.options?.[k], key);
}

test.describe('terminal font + copy/paste menu', () => {
  test.setTimeout(25_000);

  test('size stepper changes xterm font size live', async ({ page, router }) => {
    await openTerminal(page, router.url);

    // Default is 12px.
    expect(await termOption(page, 'fontSize')).toBe(12);

    // The font controls live in the menubar "Font" menu; the stepper
    // keeps the menu open across clicks so several steps land in a row.
    await page.locator('[data-testid="term-menu-font-btn"]').click();
    await expect(page.locator('[data-testid="term-menu-font"]')).toBeVisible();
    await expect(page.locator('[data-testid="term-menu-size-val"]')).toHaveText('12');

    await page.locator('[data-testid="term-menu-size-inc"]').click();
    await page.locator('[data-testid="term-menu-size-inc"]').click();
    await expect(page.locator('[data-testid="term-menu-size-val"]')).toHaveText('14');
    await expect.poll(() => termOption(page, 'fontSize')).toBe(14);

    await page.locator('[data-testid="term-menu-size-dec"]').click();
    await expect(page.locator('[data-testid="term-menu-size-val"]')).toHaveText('13');
    await expect.poll(() => termOption(page, 'fontSize')).toBe(13);
  });

  test('picking a bundled font loads its woff2 and applies it', async ({ page, router }) => {
    await openTerminal(page, router.url);

    await page.locator('[data-testid="term-menu-font-btn"]').click();
    await page.locator('[data-testid="term-menu-font-fira-code"]').click();

    // The woff2 loads asynchronously; xterm swaps to the Fira stack
    // only once the FontFace resolves.
    await expect
      .poll(() => termOption(page, 'fontFamily'), { timeout: 8_000 })
      .toContain('Fira Code');

    // The choice is window-wide: a second tab opens already on it.
    // Poll — the new tab's xterm publishes __washTerm a frame after mount.
    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);
    await expect
      .poll(
        () =>
          page
            .locator('[data-testid="term-host"]')
            .nth(1)
            .evaluate((h: any) => h.__washTerm?.options?.fontFamily ?? ''),
        { timeout: 8_000 },
      )
      .toContain('Fira Code');
  });

  test('right-click is the menu: Copy, Paste, Paste selection', async ({ page, context, router }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: router.url });
    const host = await openTerminal(page, router.url);

    // ---- Copy: the menu's Copy puts the live selection on the clipboard ----
    await host.click();
    await page.keyboard.type('wash-copy-marker');
    await host.evaluate((h: any) => h.__washTerm?.selectAll());
    await host.click({ button: 'right' });
    await expect(page.locator('[data-testid="term-context-menu"]')).toBeVisible();
    await page.locator('[data-testid="term-ctx-copy"]').click();
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toContain('wash-copy-marker');

    // ---- Paste: on this secure origin a system clipboard changed outside
    // wash wins (docs/CLIPBOARD.md "last change wins") ----
    await page.evaluate(() => (window as any).wash.clipboardSetText('wash-paste-marker'));
    await page.evaluate(() => navigator.clipboard.writeText('sys-paste-marker'));
    await host.click({ button: 'right' });
    await page.locator('[data-testid="term-ctx-paste"]').click();
    await expect.poll(() => bufferText(page), { timeout: 5_000 }).toContain('sys-paste-marker');
    // The system read is folded back into the wash clipboard.
    await expect
      .poll(() => page.evaluate(() => (window as any).wash.clipboardGetText()), { timeout: 5_000 })
      .toBe('sys-paste-marker');

    // ---- Paste selection: the last highlighted text ----
    await page.evaluate(() => (window as any).wash.selectionSetText('sel-paste-marker'));
    await host.click({ button: 'right' });
    await page.locator('[data-testid="term-ctx-paste-selection"]').click();
    await expect.poll(() => bufferText(page), { timeout: 5_000 }).toContain('sel-paste-marker');
  });
});
