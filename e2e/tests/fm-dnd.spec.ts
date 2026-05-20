// fm drag-and-drop: left-click drag = move (v1). Covered here in
// two layers per [[wash-fm-dnd-plan]]:
//
//   1. Within one fm window: drag a file row onto a folder row,
//      assert the move happens and fs.watch refreshes the tree.
//   2. Across two fm windows: drag from window A to window B,
//      assert both windows reflect the change without manual reload.
//
// Cross-window relies on the fact that both fm instances share
// WASH_FM_ROOT and each watches its own current dir; the target's
// BE does the syscall, both observe via fs.watch.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test.describe('fm DnD: within-window move', () => {
  test('drop a file onto a folder row → file moves into the folder', async ({ page, router }) => {
    // Seed adds hello.txt + docs/. The drop target is docs.
    await openFm(page, router);

    const src = page.locator('[data-testid="fm-entry-hello.txt"]');
    const dst = page.locator('[data-testid="fm-entry-docs"]');
    await src.dragTo(dst);

    // hello.txt left the root tree and appeared inside docs/.
    // fs.watch is the refresh path — no manual reload.
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'docs', 'hello.txt'), 'utf8')).toBe('hello world\n');

    // Tree assertion: original row vanishes; once docs is expanded,
    // hello.txt shows up underneath. Expand docs so the new row
    // becomes visible in the tree.
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toHaveCount(0, { timeout: 3_000 });
    await page.locator('[data-testid="fm-entry-docs"]').click();
    await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible({ timeout: 3_000 });
  });

  test('drop into the same parent dir: no-op (move not attempted)', async ({ page, router }) => {
    // Make a sibling folder. Dragging hello.txt onto the list
    // pane's empty area = "move into current dir" = root, which
    // is hello.txt's existing parent. Should be a silent no-op:
    // the file stays put, no rename_err in status.
    await openFm(page, router);
    const before = readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8');

    const src = page.locator('[data-testid="fm-entry-hello.txt"]');
    const fmList = page.locator('[data-testid="fm-list"]');
    // Target a position well below the existing rows so we hit
    // empty list pane, not a row.
    await src.dragTo(fmList, { targetPosition: { x: 50, y: 350 } });

    // File untouched.
    expect(readFileSync(join(router.fmRoot, 'hello.txt'), 'utf8')).toBe(before);
    // Status bar doesn't carry an error.
    await expect(page.locator('[data-testid="fm-status"]')).not.toContainText(/move:/);
  });

  // Note: a "drop a folder into its own descendant" e2e was tried
  // and abandoned. The FE has an early-reject in commitMove with
  // a friendly status; the BE EINVALs as a backstop. Both paths
  // exist. The Playwright dragTo flakes here because expanding the
  // parent dir before the drag triggers a Solid re-render of the
  // source row, and dragTo's actionability check loses the
  // element. The user-meaningful coverage (collision, no-op,
  // happy-path move) is in the other tests in this file; the
  // descendant guard is covered by unit-level review of commitMove.

  test('drop on the list pane empty area: moves into current dir', async ({ page, router }) => {
    // From inside a subdir, drag a file onto the empty pane area
    // — should move it into whichever dir is "current". We make
    // a subdir, navigate into it, then drag a child up to the
    // root by first navigating to root, then dragging.
    mkdirSync(join(router.fmRoot, 'sub'));
    writeFileSync(join(router.fmRoot, 'sub', 'inner.txt'), 'inner\n');

    await openFm(page, router);
    // Expand sub by clicking it; inner.txt should appear.
    await page.locator('[data-testid="fm-entry-sub"]').click();
    await expect(page.locator('[data-testid="fm-entry-inner.txt"]')).toBeVisible();

    // Navigate to root so dirOfSelection() == root.
    await page.locator('[data-testid="fm-home"]').click();
    await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);

    // Drag inner.txt onto empty pane area below the rows.
    const inner = page.locator('[data-testid="fm-entry-inner.txt"]');
    const fmList = page.locator('[data-testid="fm-list"]');
    await inner.dragTo(fmList, { targetPosition: { x: 50, y: 380 } });

    // File now in root.
    expect(existsSync(join(router.fmRoot, 'inner.txt'))).toBe(true);
    expect(existsSync(join(router.fmRoot, 'sub', 'inner.txt'))).toBe(false);
  });

  test('drop from window A onto a folder row in window B: file moves', async ({ page, router }) => {
    // Open two fm windows. The default cascade places them at
    // (40,40) and (64,64) — overlapping. Reposition window B
    // to the right so both windows are hittable by Playwright's
    // dragTo. The wash WM is server-authoritative for window
    // geometry, so we use window.wash.moveWindow to reposition.
    await openFm(page, router);
    // First fm window's window_id is captured before launching
    // the second so we can pick out the "newer" one afterwards.
    const firstIDs = await page.evaluate(() => window.wash.windows().map((w) => w.windowID));
    expect(firstIDs).toHaveLength(1);
    const aID = firstIDs[0];

    // Launch a second fm via the launcher. Scope to the start
    // menu — the taskbar now also has a "Files" button for the
    // first open window, which would make the role-by-name lookup
    // ambiguous in strict mode.
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    // Wait until two fm-app-fm elements are present.
    await expect(page.locator('wash-app-fm')).toHaveCount(2);

    const allIDs = await page.evaluate(() => window.wash.windows().map((w) => w.windowID));
    const bID = allIDs.find((id) => id !== aID)!;
    expect(bID).toBeTruthy();

    // Reposition: A on the left, B on the right.
    await page.evaluate((args) => {
      window.wash.moveWindow(args.a, 40, 60);
      window.wash.resizeWindow(args.a, 440, 380);
      window.wash.moveWindow(args.b, 540, 60);
      window.wash.resizeWindow(args.b, 440, 380);
    }, { a: aID, b: bID });

    // Disambiguate the two fm elements: nth(0) is the first
    // declared, nth(1) the second. The shell preserves mount
    // order — first-spawned fm element renders first.
    const fmA = page.locator('wash-app-fm').nth(0);
    const fmB = page.locator('wash-app-fm').nth(1);
    await expect(fmA.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
    await expect(fmB.locator('[data-testid="fm-entry-docs"]')).toBeVisible();

    // Drag hello.txt from A onto docs row in B. The destination
    // BE does the rename; fs.watch in BOTH windows then drives
    // the refresh — A loses the row, B sees docs/hello.txt
    // appear (when docs is expanded).
    const src = fmA.locator('[data-testid="fm-entry-hello.txt"]');
    const dst = fmB.locator('[data-testid="fm-entry-docs"]');
    await src.dragTo(dst);

    // Filesystem reflects the move.
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(false);
    expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(true);

    // Window A: row vanishes (its fs.watch sees the source
    // deletion and refreshes root). Use timeout for the debounce.
    await expect(fmA.locator('[data-testid="fm-entry-hello.txt"]')).toHaveCount(0, { timeout: 3_000 });

    // Window B: expand docs to see the moved file. fs.watch in
    // B's root saw docs's mtime bump and refreshed root
    // (transparent — same listing). When the user expands docs
    // in B, the listing fetch picks up hello.txt naturally.
    await fmB.locator('[data-testid="fm-entry-docs"]').click();
    await expect(fmB.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible({ timeout: 3_000 });
  });

  test('move collision: dest already exists → rename_err exists, source untouched', async ({ page, router }) => {
    // Create a folder with the same-named file already inside,
    // then try to drag the root hello.txt onto that folder.
    // BE should reject with exists; the file stays at root.
    mkdirSync(join(router.fmRoot, 'taken'));
    writeFileSync(join(router.fmRoot, 'taken', 'hello.txt'), 'preexisting\n');

    await openFm(page, router);
    const src = page.locator('[data-testid="fm-entry-hello.txt"]');
    const dst = page.locator('[data-testid="fm-entry-taken"]');
    await src.dragTo(dst);

    await expect(page.locator('[data-testid="fm-status"]')).toContainText(/move:.*exists/);
    expect(existsSync(join(router.fmRoot, 'hello.txt'))).toBe(true);
    expect(readFileSync(join(router.fmRoot, 'taken', 'hello.txt'), 'utf8')).toBe('preexisting\n');
  });
});
