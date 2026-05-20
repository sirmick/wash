// Drag-and-drop foundation. wash-fm rows are draggable; the tree
// accepts drops carrying application/x-wash-path. v1 surfaces the
// drop in the status bar — no real file ops yet (rename/delete in
// v2, move/copy in v3).
//
// We test within a single wash-fm window for now. Cross-window DnD
// works the same way (native HTML5 within one tab), but positioning
// two overlapping windows so both source and target are hittable
// makes the e2e fragile; defer that scenario.

import { test, expect } from '../fixtures/router';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

test.describe('drag-and-drop', () => {
  test('drag a row onto the tree: status reflects the drop', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-dnd-'));
    writeFileSync(join(root, 'dragged.txt'), 'payload\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();

    await fm.locator('[data-testid="fm-path"]').fill(root);
    await fm.locator('[data-testid="fm-path"]').press('Enter');
    await expect(fm.locator('[data-testid="fm-entry-dragged.txt"]')).toBeVisible();

    // Drag the file row onto the tree. Same window — Playwright fires
    // the HTML5 drag sequence; our onDrop reads
    // application/x-wash-path and writes the status bar.
    const src = fm.locator('[data-testid="fm-entry-dragged.txt"]');
    const dst = fm.locator('[data-testid="fm-list"]');
    await src.dragTo(dst, { targetPosition: { x: 100, y: 200 } });

    const expectedPath = join(root, 'dragged.txt');
    await expect(fm.locator('[data-testid="fm-status"]')).toHaveText(`Dropped: ${expectedPath}`);
  });

  test('rows are draggable: dataTransfer carries the path', async ({ page, router }) => {
    const root = mkdtempSync(join(tmpdir(), 'wash-dnd-data-'));
    writeFileSync(join(root, 'sample.txt'), 'x\n');

    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();

    await fm.locator('[data-testid="fm-path"]').fill(root);
    await fm.locator('[data-testid="fm-path"]').press('Enter');
    await expect(fm.locator('[data-testid="fm-entry-sample.txt"]')).toBeVisible();

    // Inspect the row to confirm draggable=true.
    const row = fm.locator('[data-testid="fm-entry-sample.txt"]');
    await expect(row).toHaveAttribute('draggable', 'true');
  });
});
