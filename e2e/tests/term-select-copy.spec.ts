// wash-term selection UX: double-click word selection breaks on symbols
// (not just whitespace), and plain Ctrl+C copies the selection when one
// exists (falling back to SIGINT when it doesn't) on non-mac platforms.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

// Must mirror TERM_WORD_SEPARATORS in web/lib/src/terminal.tsx: every
// ASCII punctuation char except '_' (kept a word char), plus space.
const EXPECTED_WORD_SEPARATORS = ' !"#$%&\'()*+,-./:;<=>?@[\\]^`{|}~';

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

test.describe('terminal selection + copy keybinding', () => {
  test.setTimeout(25_000);

  test('xterm is configured to break word selection on symbols', async ({ page, router }) => {
    await openTerminal(page, router.url);
    const sep = await page
      .locator('[data-testid="term-host"]')
      .first()
      .evaluate((h: any) => h.__washTerm?.options?.wordSeparator);
    expect(sep).toBe(EXPECTED_WORD_SEPARATORS);
    // Sanity: the chars that make xterm's default grab a whole path are
    // now boundaries, while identifiers stay intact.
    expect(sep).toContain('/');
    expect(sep).toContain('.');
    expect(sep).not.toContain('_');
  });

  test('Ctrl+C copies the selection and clears it (else SIGINT)', async ({ page, context, router }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write'], { origin: router.url });
    const host = await openTerminal(page, router.url);

    await host.click();
    await page.keyboard.type('ctrl-c-copy-marker');
    // Wait for the PTY echo to actually render the typed text into the
    // xterm buffer BEFORE selecting. Under parallel load the echo
    // round-trip lags, and selectAll() over a half-rendered line copied
    // a partial marker (e.g. "…$ c") → the clipboard assertion below
    // flaked. selectAll is only meaningful once the line is complete.
    await expect.poll(() => bufferText(page)).toContain('ctrl-c-copy-marker');
    await host.evaluate((h: any) => h.__washTerm?.selectAll());
    await expect.poll(() => host.evaluate((h: any) => !!h.__washTerm?.hasSelection())).toBe(true);

    // Plain Ctrl+C with a live selection copies (mirrors to the system
    // clipboard while the keypress gesture is live) and clears it.
    await page.keyboard.press('Control+c');
    await expect
      // Inherit the config's expect timeout (15s) rather than a tight 5s: the
      // Ctrl+C → copy → system-clipboard mirror is a round-trip that's slow
      // under parallel load (flaked here at 5s, passed on retry).
      .poll(() => page.evaluate(() => navigator.clipboard.readText()))
      .toContain('ctrl-c-copy-marker');
    await expect
      .poll(() => host.evaluate((h: any) => !!h.__washTerm?.hasSelection()))
      .toBe(false);
  });
});
