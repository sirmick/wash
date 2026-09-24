// fm keyboard + cut affordances (docs/Review-findings.md P2 → fm).
//
// Four gaps, one spec: cut items looked identical to everything else until
// the paste landed; Ctrl+A only ever meant the tree even while you were
// clicking tiles in the folder grid; and Ctrl+L / Ctrl+H / Ctrl+N — the
// address bar, hidden files and a new file — were unbound (only
// Ctrl+Shift+N, new folder, existed).

import { test, expect } from '../fixtures/router';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'notes.txt'), 'note\n');
  writeFileSync(join(root, 'other.txt'), 'other\n');
  writeFileSync(join(root, '.hidden'), 'secret\n');
  mkdirSync(join(root, 'target'), { recursive: true });
  mkdirSync(join(root, 'gallery'), { recursive: true });
  writeFileSync(join(root, 'gallery', 'g1.txt'), '1\n');
  writeFileSync(join(root, 'gallery', 'g2.txt'), '2\n');
  writeFileSync(join(root, 'gallery', 'g3.txt'), '3\n');
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'bulk'], fmRoot: true, fmSeed: seed },
});

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-notes.txt"]')).toBeVisible();
}

const row = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-entry-${name}"]`);
const tile = (page: import('@playwright/test').Page, name: string) =>
  page.locator(`[data-testid="fm-tile-${name}"]`);

test.describe('fm shortcuts and cut affordance', () => {
  test.setTimeout(45_000);

  test('cut rows render dimmed until the paste lands', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');

    await row(page, 'notes.txt').click();
    await fm.press('Control+x');
    await expect(row(page, 'notes.txt')).toHaveAttribute('data-dimmed', 'true');
    // A sibling that is not on the clipboard is untouched.
    await expect(row(page, 'other.txt')).not.toHaveAttribute('data-dimmed', 'true');

    // Paste into the target folder. Once the move lands the cut has
    // resolved, so NOTHING in the window is dimmed any more. (The row's
    // own new home is asserted on disk: whether the tree has re-listed
    // the destination yet is the watcher's business, not this spec's.)
    const from = router.logCursor();
    await row(page, 'target').click();
    await fm.press('Control+v');
    await router.waitForLog(/bulk-ops job=\S+ op=move status=done/, 15_000, from);
    await expect(page.locator('wash-app-fm [data-dimmed="true"]')).toHaveCount(0);
    expect(existsSync(join(router.fmRoot, 'target', 'notes.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'notes.txt'))).toBe(false);
  });

  test('a copy dims nothing, and Escape abandons a pending cut', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');

    await row(page, 'notes.txt').click();
    await fm.press('Control+c');
    await expect(row(page, 'notes.txt')).not.toHaveAttribute('data-dimmed', 'true');

    await fm.press('Control+x');
    await expect(row(page, 'notes.txt')).toHaveAttribute('data-dimmed', 'true');
    await fm.press('Escape'); // clears the selection
    await fm.press('Escape'); // clears the cut
    await expect(row(page, 'notes.txt')).not.toHaveAttribute('data-dimmed', 'true');
  });

  test('Ctrl+H toggles hidden files and Ctrl+L focuses the path bar', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');

    await expect(row(page, '.hidden')).toHaveCount(0);
    await fm.press('Control+h');
    await expect(row(page, '.hidden')).toBeVisible();
    await fm.press('Control+h');
    await expect(row(page, '.hidden')).toHaveCount(0);

    await fm.press('Control+l');
    const pathBar = page.locator('[data-testid="fm-path"]');
    await expect(pathBar).toBeFocused();
    // Selected, so typing replaces the path rather than appending to it.
    await pathBar.press('a');
    await expect(pathBar).toHaveValue('a');
  });

  test('Ctrl+N starts a new file, Ctrl+Shift+N a new folder', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');

    await fm.press('Control+n');
    await expect(page.locator('[data-testid="fm-pending-new-file"]')).toBeVisible();
    await page.locator('[data-testid="fm-pending-new-input"]').fill('fresh.txt');
    await page.locator('[data-testid="fm-pending-new-input"]').press('Enter');
    await expect(row(page, 'fresh.txt')).toBeVisible({ timeout: 15_000 });

    await fm.press('Control+Shift+n');
    await expect(page.locator('[data-testid="fm-pending-new-folder"]')).toBeVisible();
    await page.locator('[data-testid="fm-pending-new-input"]').press('Escape');
  });

  test('Ctrl+A selects the grid tiles once the grid dock has been clicked', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');

    // Single-clicking a folder shows its contents in the preview grid.
    await row(page, 'gallery').click();
    await expect(tile(page, 'g1.txt')).toBeVisible({ timeout: 15_000 });

    // Still "tree" focus: Ctrl+A takes the tree's visible rows, so the
    // top-level siblings come along.
    await fm.press('Control+a');
    await expect(row(page, 'notes.txt')).toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'target')).toHaveAttribute('data-selected', 'true');

    // Click a tile — now the grid owns the dock, and Ctrl+A means its tiles.
    await tile(page, 'g1.txt').click();
    await fm.press('Control+a');
    await expect(tile(page, 'g1.txt')).toHaveAttribute('data-selected', 'true');
    await expect(tile(page, 'g2.txt')).toHaveAttribute('data-selected', 'true');
    await expect(tile(page, 'g3.txt')).toHaveAttribute('data-selected', 'true');
    // …and only those: the tree's rows dropped out of the selection.
    await expect(row(page, 'notes.txt')).not.toHaveAttribute('data-selected', 'true');
    await expect(row(page, 'target')).not.toHaveAttribute('data-selected', 'true');
  });
});
