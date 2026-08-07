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

async function activeBufferText(page: Page): Promise<string> {
  return await page
    .locator('[data-testid="term-host"]')
    .locator('visible=true')
    .evaluate((host: any) => {
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
  test.setTimeout(20_000);

  test('launches, shows a prompt, echoes a typed command', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();

    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();

    const term = page.locator('wash-app-term');
    await expect(term).toBeVisible();
    const host = page.locator('[data-testid="term-host"]').first();
    await expect(host).toBeVisible();

    await expect.poll(() => bufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    await host.click();
    await page.keyboard.type('printf wash-rocks');
    await page.keyboard.press('Enter');

    await expect.poll(() => bufferText(page), { timeout: 5_000 }).toContain('wash-rocks');
  });

  test('+ button opens a second tab, each preserves its own buffer', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    await expect(page.locator('wash-app-term')).toBeVisible();

    // Wait for tab 1's prompt.
    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await page.locator('[data-testid="term-host"]').first().click();
    await page.keyboard.type('printf tab-one');
    await page.keyboard.press('Enter');
    await expect.poll(() => activeBufferText(page), { timeout: 5_000 }).toContain('tab-one');

    // Open a second tab.
    await page.locator('[data-testid="term-new-tab"]').click();
    // Two hosts exist now.
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Tab 2 is active. Wait for its prompt.
    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    await page.keyboard.type('printf tab-two');
    await page.keyboard.press('Enter');
    await expect.poll(() => activeBufferText(page), { timeout: 5_000 }).toContain('tab-two');

    // Tab 1 should not show tab-two's output. Switch back via its tab
    // button. Tab buttons are data-testid="term-tab-{channelID}";
    // pick the first by index, not id (id is router-allocated).
    const tabButtons = page.locator('[data-testid^="term-tab-"][data-testid$="-1"]');
    // The first tab is the lower-id; the tab bar puts it first.
    const tabs = page.locator('button[data-testid^="term-tab-"]:not([data-testid^="term-tab-close"])');
    await tabs.first().click();
    await expect.poll(() => activeBufferText(page)).toContain('tab-one');
    await expect.poll(() => activeBufferText(page)).not.toContain('tab-two');
  });

  test('closing a tab leaves the window with the other tab', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    await expect(page.locator('wash-app-term')).toBeVisible();
    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    await page.locator('[data-testid="term-new-tab"]').click();
    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(2);

    // Close the SECOND tab via its × inside the tab button.
    const closeButtons = page.locator('span[data-testid^="term-tab-close-"]');
    await expect(closeButtons).toHaveCount(2);
    await closeButtons.last().click();
    // Every close is confirmed now (docs/TERM_LAYOUT.md) — answer the dialog.
    await page.locator('[data-testid="term-close-confirm-ok"]').click();

    await expect(page.locator('[data-testid="term-host"]')).toHaveCount(1);
    // Window still there.
    await expect(page.locator('wash-app-term')).toBeVisible();
  });
});
