// fm "Open terminal here" (docs/Review-findings.md P2 → fm). fm could open
// files in apps but never gave you a shell where you were standing; getting
// a terminal in the folder you were browsing meant launching wash-term and
// re-typing the path.
//
// The toolbar button and the context menu now spawn com.wash.term with the
// folder as its `--open` argv (Conn.SpawnRequestOpen). Both halves: the fm
// BE's `fm: open_terminal dir=…` audit line carrying the right directory,
// and a term window appearing.
//
// TODO: the term track is teaching wash-term to honour `--open <dir>` as
// the first tab's cwd. Once both land, assert the SHELL's cwd here (`pwd`
// in the spawned tab) rather than only the spawn's argv.

import { test, expect } from '../fixtures/router';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

function seed(root: string): void {
  writeFileSync(join(root, 'notes.txt'), 'hello\n');
  mkdirSync(join(root, 'project'), { recursive: true });
  writeFileSync(join(root, 'project', 'main.go'), 'package main\n');
}

test.use({
  routerOpts: { apps: ['session', 'fm', 'term'], fmRoot: true, fmSeed: seed },
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

const escapeRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

test.describe('fm open terminal here', () => {
  test.setTimeout(45_000);

  test('the toolbar button spawns a terminal for the viewed folder', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await page.locator('[data-testid="fm-open-terminal"]').click();
    await router.waitForLog(
      new RegExp(`fm: open_terminal dir="${escapeRe(router.fmRoot)}" app=com\\.wash\\.term`),
      10_000,
      from,
    );
    await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 20_000 });
  });

  test('the context menu spawns a terminal for the clicked folder', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await page.locator('[data-testid="fm-entry-project"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-open-terminal"]').click();
    await router.waitForLog(
      new RegExp(`fm: open_terminal dir="${escapeRe(join(router.fmRoot, 'project'))}" app=com\\.wash\\.term`),
      10_000,
      from,
    );
    await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 20_000 });
  });

  // A file row means "a terminal where this file lives" — the BE resolves
  // the path to its parent. Its own test: the spawned term window covers
  // fm, so a second right-click in the same test can't reach a row.
  test('the context menu on a file row uses the folder containing it', async ({ page, router }) => {
    await openFm(page, router);

    const from = router.logCursor();
    await page.locator('[data-testid="fm-entry-notes.txt"]').click({ button: 'right' });
    await page.locator('[data-testid="fm-ctx-open-terminal"]').click();
    await router.waitForLog(
      new RegExp(`fm: open_terminal dir="${escapeRe(router.fmRoot)}" app=com\\.wash\\.term`),
      10_000,
      from,
    );
    await expect(page.locator('wash-app-term')).toBeVisible({ timeout: 20_000 });
  });
});
