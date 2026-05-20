// wash-term: launches from the chrome start menu, the BE forks bash
// via creack/pty, raw bytes round-trip through the wash channel, and
// xterm.js renders them. The test reads xterm's buffer to verify
// real shell output reached the page.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function bufferText(page: Page): Promise<string> {
  return await page.locator('[data-testid="term-host"]').first().evaluate((host: any) => {
    const term = host.__washTerm;
    if (!term) return '';
    const buf = term.buffer.active;
    let out = '';
    for (let y = 0; y < buf.length; y++) {
      const line = buf.getLine(y);
      if (!line) continue;
      out += line.translateToString(true) + '\n';
    }
    return out;
  });
}

test.describe('terminal app', () => {
  test('launches, shows a prompt, echoes a typed command', async ({ page, router }) => {
    test.setTimeout(20_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.getByRole('button', { name: /Terminal/ }).click();

    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    const host = page.locator('[data-testid="term-host"]').first();
    await expect(host).toBeVisible();

    // Wait for the shell prompt to show up in xterm's buffer.
    await expect.poll(() => bufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // Type a command (xterm onData → BE writeRaw → pty stdin).
    // Click into the terminal first so xterm has focus.
    await host.click();
    await page.keyboard.type('printf wash-rocks');
    await page.keyboard.press('Enter');

    await expect.poll(() => bufferText(page), { timeout: 5_000 }).toContain('wash-rocks');
  });
});
