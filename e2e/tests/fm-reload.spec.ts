// Toolbar Reload must refresh the directory the tree is SHOWING. After
// navigating into a folder, path() is that folder; the button used to call
// invalidateAndList(parentPath(path())) — re-listing the parent and never the
// folder being looked at (docs/Review-findings.md, P1 → fm).
//
// Both halves: the FE shows the new row, and the BE's `fm: list path=…`
// audit line names the viewed folder — and NOT its parent — as the directory
// that got re-requested.
//
// fs.watch would also surface a new file, so it is taken out of the picture
// for the assertion window: the shared wash-fswatch service is SIGSTOPped
// before the file is written and SIGCONTed after. While it is stopped no
// fs_event reaches fm, so the row can only appear because Reload listed the
// right directory.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

// pidsWithArgv finds processes whose argv mentions needle — the staged
// wash-fswatch symlink under this test's apps dir.
function pidsWithArgv(needle: string): number[] {
  const out: number[] = [];
  for (const pid of readdirSync('/proc')) {
    if (!/^\d+$/.test(pid)) continue;
    let argv = '';
    try {
      argv = readFileSync(`/proc/${pid}/cmdline`, 'utf8');
    } catch {
      continue;
    }
    if (argv.includes(needle)) out.push(parseInt(pid, 10));
  }
  return out;
}

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('Reload re-lists the folder being viewed, not its parent', async ({ page, router }) => {
  await openFm(page, router);
  const docs = join(router.fmRoot, 'docs');

  // Navigate INTO docs: path() is now docs.
  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(docs);

  // Take fs.watch out of the picture, then create a file on disk.
  const watchers = pidsWithArgv(`${router.appsDir}/wash-fswatch`);
  expect(watchers.length, 'wash-fswatch must be running to be paused').toBeGreaterThan(0);
  for (const pid of watchers) process.kill(pid, 'SIGSTOP');
  try {
    writeFileSync(join(docs, 'fresh.txt'), 'fresh\n');
    // Past fm's 100 ms watch debounce: nothing shows, so the watch really is
    // out of the loop and whatever appears next is Reload's doing.
    await page.waitForTimeout(500);
    await expect(page.locator('[data-testid="fm-entry-fresh.txt"]')).toHaveCount(0);

    const from = router.logCursor();
    await page.locator('[data-testid="fm-reload"]').click();
    await expect(page.locator('[data-testid="fm-entry-fresh.txt"]')).toBeVisible({ timeout: 5_000 });

    // BE half: the viewed folder was listed…
    await router.waitForLog(new RegExp(`fm: list path="${escapeRe(docs)}"`), 5_000, from);
    // …and its parent was not.
    const since = router.log().slice(from);
    expect(since).not.toMatch(new RegExp(`fm: list path="${escapeRe(router.fmRoot)}"`));
  } finally {
    for (const pid of watchers) {
      try { process.kill(pid, 'SIGCONT'); } catch { /* gone */ }
    }
  }
});

test('Reload at the tree root re-lists the root, not the directory above it', async ({ page, router }) => {
  await openFm(page, router);
  // path() is the sandbox root — a listed dir. The old code requested
  // parentPath(root), which the confined BE rejects as outside_root.
  const from = router.logCursor();
  await page.locator('[data-testid="fm-reload"]').click();
  await router.waitForLog(new RegExp(`fm: list path="${escapeRe(router.fmRoot)}"`), 5_000, from);
  const since = router.log().slice(from);
  expect(since).not.toMatch(/outside/);
  expect(since).not.toMatch(new RegExp(`fm: list path="${escapeRe(join(router.fmRoot, '..'))}"`));
  // The listing is intact after the refresh.
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-docs"]')).toBeVisible();
});
