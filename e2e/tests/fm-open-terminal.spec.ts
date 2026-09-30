// fm "Open terminal here" (docs/Review-findings.md P2 → fm). fm could open
// files in apps but never gave you a shell where you were standing; getting
// a terminal in the folder you were browsing meant launching wash-term and
// re-typing the path.
//
// Two ways in. The row context menu spawns com.wash.term for the CLICKED
// folder, a new window each time (`fm: open_terminal dir=…`). The toolbar's
// Terminal icon is the Places bar (docs/PLACES.md): the first click opens a
// terminal in the VIEWED folder and binds it to this window, and every click
// after brings that same terminal back rather than opening another.
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

  test('the Places terminal icon opens one terminal, bound, and reuses it', async ({ page, router }) => {
    await openFm(page, router);
    const fm = page.locator('wash-app-fm');
    const icon = fm.getByTestId('places-term');
    await expect(icon).toHaveAttribute('data-bound', 'false');
    await expect(icon).toHaveAttribute('title', `Open Terminal here · ${router.fmRoot}`);

    const from = router.logCursor();
    await icon.click();
    await router.waitForLog(
      new RegExp(`places: com\\.wash\\.fm open com\\.wash\\.term dir="${escapeRe(router.fmRoot)}"`),
      10_000,
      from,
    );
    await expect(page.locator('wash-app-term')).toHaveCount(1, { timeout: 20_000 });

    // Bound, and visibly so from BOTH ends, in the same group.
    await expect(icon).toHaveAttribute('data-bound', 'true');
    const term = page.locator('wash-app-term');
    await expect(term.getByTestId('places-fm')).toHaveAttribute('data-bound', 'true');
    const group = await fm.getByTestId('places-bar').getAttribute('data-group');
    expect(group).toBeTruthy();
    await expect(term.getByTestId('places-bar')).toHaveAttribute('data-group', group!);

    // The point of binding: a second click brings THAT terminal back. The
    // new terminal opened on top of Files' corner, so bring Files forward
    // first, as a person would.
    const fmWin = Number(await fm.getAttribute('data-wash-window'));
    await page.evaluate((w) => window.wash.focusWindow(w), fmWin);
    const again = router.logCursor();
    await icon.click();
    await router.waitForLog(/places: com\.wash\.fm show com\.wash\.term /, 10_000, again);
    await expect(page.locator('wash-app-term')).toHaveCount(1);
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
