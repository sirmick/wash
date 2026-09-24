// Switching tabs keeps each tab's scroll position. captureActiveState
// already recorded scrollTop on the way out; the active-tab effect now
// restores it on the way back (it used to be restored only by session
// restore, so a switched-away tab always came back at the top).

import { test, expect } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  const lines = Array.from({ length: 400 }, (_, i) => `line ${i + 1}`);
  writeFileSync(join(root, 'long.txt'), lines.join('\n') + '\n');
  writeFileSync(join(root, 'short.txt'), 'short\n');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

test('tab switch restores the scroll position', async ({ page, router }) => {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await editor.locator('[data-testid="edit-entry-long.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('line 1');

  const scroller = editor.locator('.cm-scroller');
  await scroller.evaluate((el) => { el.scrollTop = 3000; });
  // CM re-measures the newly rendered lines and nudges scrollTop to keep
  // its anchor line put; read the settled value, not the one we wrote.
  await page.evaluate(() => new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r))));
  const scrolled = await scroller.evaluate((el) => el.scrollTop);
  expect(scrolled).toBeGreaterThan(1000);

  await editor.locator('[data-testid="edit-entry-short.txt"]').dblclick();
  await expect(editor.locator('.cm-content')).toContainText('short');
  expect(await scroller.evaluate((el) => el.scrollTop)).toBe(0);

  await editor.locator(`[data-testid="edit-tab-${join(router.fmRoot, 'long.txt')}"]`).click();
  await expect(editor.locator('.cm-content')).toContainText(/line \d+/);
  await expect.poll(() => scroller.evaluate((el) => el.scrollTop)).toBe(scrolled);
});
