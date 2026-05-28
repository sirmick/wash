// wash-edit terminal pane: launches the editor, opens the
// terminal panel via the Terminal menu, types a command, and
// reads xterm's buffer (exposed as `__washTerm` on each terminal
// host) to confirm output round-tripped through the PTY.
//
// Same pattern wash-term's spec uses; this re-runs the contract
// against the editor's embedded terminal so we know its BE's PTY
// handlers + raw-channel wiring match.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';

async function activeBufferText(page: Page): Promise<string> {
  return await page
    .locator('wash-app-edit [data-testid^="edit-term-host-"]')
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

async function openEditor(page: Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor).toBeVisible();
  return editor;
}

test.describe('wash-edit terminal pane', () => {
  test.use({ routerOpts: { fmRoot: true } });
  test.setTimeout(20_000);

  test('Terminal menu → New Terminal: pane appears, shell prompt shows', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    // Open the Terminal menu.
    await editor.locator('[data-testid="edit-menubar-terminal"]').click();
    await page.locator('[data-testid="edit-menu-term-new"]').click();

    // Pane is visible; one terminal tab.
    await expect(editor.locator('[data-testid="edit-term-pane"]')).toBeVisible();
    await expect(editor.locator('[data-testid^="edit-term-tab-t-"]')).toHaveCount(1);

    // Wait for the shell prompt to render.
    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // Type a command, read it back.
    const host = editor.locator('[data-testid^="edit-term-host-"]').locator('visible=true');
    await host.click();
    await page.keyboard.type('printf edit-rocks');
    await page.keyboard.press('Enter');
    await expect.poll(() => activeBufferText(page), { timeout: 5_000 }).toContain('edit-rocks');
  });

  test('Ctrl+` toggles the panel; the toggle persists', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.click();

    // Open via shortcut.
    await page.keyboard.press('Control+`');
    await expect(editor.locator('[data-testid="edit-term-pane"]')).toBeVisible();

    // Toggle off.
    await page.keyboard.press('Control+`');
    await expect(editor.locator('[data-testid="edit-term-pane"]')).not.toBeVisible();
  });

  test('+ button opens a second terminal, each preserves its own buffer', async ({ page, router }) => {
    const editor = await openEditor(page, router);
    await editor.locator('[data-testid="edit-menubar-terminal"]').click();
    await page.locator('[data-testid="edit-menu-term-new"]').click();

    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);

    // Type into tab 1.
    const host1 = editor.locator('[data-testid^="edit-term-host-"]').locator('visible=true');
    await host1.click();
    await page.keyboard.type('printf tab-one-edit');
    await page.keyboard.press('Enter');
    await expect.poll(() => activeBufferText(page), { timeout: 5_000 }).toContain('tab-one-edit');

    // New tab via + button.
    await editor.locator('[data-testid="edit-term-new"]').click();
    await expect(editor.locator('[data-testid^="edit-term-tab-t-"]')).toHaveCount(2);

    await expect.poll(() => activeBufferText(page), { timeout: 8_000 }).toMatch(/[$#%>][ ]?/);
    const host2 = editor.locator('[data-testid^="edit-term-host-"]').locator('visible=true');
    await host2.click();
    await page.keyboard.type('printf tab-two-edit');
    await page.keyboard.press('Enter');
    await expect.poll(() => activeBufferText(page), { timeout: 5_000 }).toContain('tab-two-edit');
    // Tab 2's buffer doesn't contain tab 1's text.
    await expect.poll(() => activeBufferText(page), { timeout: 2_000 }).not.toContain('tab-one-edit');
  });
});
