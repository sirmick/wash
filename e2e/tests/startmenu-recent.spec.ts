// Start menu "Recent" files (docs/Review-findings.md P2 cross-app).
//
// Every open the router routes — fm double-click (open.request), a
// spawn.request carrying a launch path, a terminal-launched `--open` — is
// reported to the session app as open.routed; the session BE keeps the last
// few per app under $XDG_STATE_HOME/wash/recent.json and the start menu
// pops them out of a row per app (files opened in the editor: "Edit ›").
// startmenu-recent-flyouts.spec.ts covers the other rows.
//
// Both halves per test: the FE (start menu rows, palette rows, editor
// windows) and the BE (router log lines for open.routed / open.request, and
// the state file on disk).

import { test, expect } from '../fixtures/router';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import type { RouterHandle } from '../fixtures/router';

function seed(root: string): void {
  writeFileSync(join(root, 'notes.md'), '# notes\n\nhello from the editor\n');
  writeFileSync(join(root, 'todo.txt'), 'buy milk\n');
}

test.use({
  routerOpts: {
    apps: ['session', 'about', 'fm', 'edit'],
    fmRoot: true,
    fmSeed: seed,
  },
});

async function openFm(page: Page, router: RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-notes.md"]')).toBeVisible();
}

// openViaFm double-clicks `name` in fm and waits for the router to report
// the routed open (BE half) and the editor to show it (FE half).
async function openViaFm(page: Page, router: RouterHandle, name: string, body: string) {
  const from = router.logCursor();
  await page.locator(`[data-testid="fm-entry-${name}"]`).dblclick();
  await router.waitForLog(new RegExp(`open\\.routed: path="[^"]*/${name.replace('.', '\\.')}" app=com\\.wash\\.edit via=open\\.request`), 15_000, from);
  const editor = page.locator('wash-app-edit').last();
  await expect(editor).toBeVisible({ timeout: 15_000 });
  await expect(editor.locator('.cm-content')).toContainText(body, { timeout: 15_000 });
  // Park the editor: it opens over fm and would intercept the next
  // double-click. Minimised windows stay in the taskbar and the count.
  await page.evaluate(() => {
    for (const w of window.wash.windows()) {
      if (w.element === 'wash-app-edit' && w.state !== 'minimized') window.wash.minimizeWindow(w.windowID, w.origin);
    }
  });
  await expect(page.locator(`[data-testid="fm-entry-${name}"]`)).toBeVisible();
}

// openEditFlyout opens the start menu and pops out its Edit row.
async function openEditFlyout(page: Page) {
  await page.locator('button[title="Apps"]').click();
  const menu = page.locator('[data-testid="start-menu"]');
  await expect(menu).toBeVisible();
  await menu.locator('[data-testid="start-menu-recent-group-edit"]').click();
  const flyout = page.locator('[data-testid="start-menu-flyout"]');
  await expect(flyout).toBeVisible();
  return { menu, flyout };
}

function stateFile(router: RouterHandle): string {
  return join(router.xdgStateHome, 'wash', 'recent.json');
}

test.describe('start menu: recent files', () => {
  test('a file opened from fm shows under Recent; clicking it re-opens through the router', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'notes.md', 'hello from the editor');

    // BE half: the session BE persisted it.
    await expect.poll(() => existsSync(stateFile(router)), { timeout: 5_000 }).toBe(true);
    const st = JSON.parse(readFileSync(stateFile(router), 'utf8')) as { recent: { path: string; app_id: string }[] };
    expect(st.recent.map((r) => r.path)).toEqual([join(router.fmRoot, 'notes.md')]);
    expect(st.recent[0].app_id).toBe('com.wash.edit');

    // FE half: the start menu's Edit row pops it out.
    const { menu, flyout } = await openEditFlyout(page);
    await expect(menu.locator('[data-testid="start-menu-recent"]')).toHaveText(/recent/i);
    const row = flyout.locator('[data-testid="start-menu-flyout-item"]');
    await expect(row).toHaveCount(1);
    await expect(row).toContainText('notes.md');

    // Clicking re-issues an open.request from the session app; the router
    // routes it to the editor exactly like the original double-click, so a
    // second editor window opens on the same file.
    const from = router.logCursor();
    await row.click();
    await expect(menu).toHaveCount(0);
    await expect(flyout).toHaveCount(0);
    await router.waitForLog(/open\.request: path="[^"]*\/notes\.md" handler=com\.wash\.edit from=com\.wash\.session/, 10_000, from);
    await router.waitForLog(/open\.routed: path="[^"]*\/notes\.md" app=com\.wash\.edit via=open\.request/, 15_000, from);
    await expect(page.locator('wash-app-edit')).toHaveCount(2, { timeout: 15_000 });
    await expect(page.locator('wash-app-edit').last().locator('.cm-content')).toContainText('hello from the editor', { timeout: 15_000 });
  });

  test('newest first, deduped by path; Remove and Clear recent via right-click', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'notes.md', 'hello from the editor');
    await openViaFm(page, router, 'todo.txt', 'buy milk');
    // Re-open notes.md: it moves to the top, no duplicate row.
    await openViaFm(page, router, 'notes.md', 'hello from the editor');

    const { menu, flyout } = await openEditFlyout(page);
    const rows = flyout.locator('[data-testid="start-menu-flyout-item"]');
    await expect(rows).toHaveCount(2);
    await expect(rows.nth(0)).toContainText('notes.md');
    await expect(rows.nth(1)).toContainText('todo.txt');

    // Remove one.
    await rows.nth(1).click({ button: 'right' });
    const ctx = page.locator('[data-testid="start-menu-recent-menu"]');
    await expect(ctx).toBeVisible();
    await ctx.locator('[data-testid="start-menu-recent-remove"]').click();
    await expect(ctx).toHaveCount(0);
    // The start menu and its flyout stay up and re-render from the BE's push.
    await expect(menu).toBeVisible();
    await expect(flyout).toBeVisible();
    await expect(rows).toHaveCount(1);
    await expect(rows.nth(0)).toContainText('notes.md');
    await expect.poll(() => (JSON.parse(readFileSync(stateFile(router), 'utf8')) as { recent: unknown[] }).recent.length).toBe(1);

    // Clear the rest: the row stays (it is where recent files will be) and
    // its flyout says there are none.
    await rows.nth(0).click({ button: 'right' });
    await expect(ctx).toBeVisible();
    await ctx.locator('[data-testid="start-menu-recent-clear"]').click();
    await expect(rows).toHaveCount(0);
    await expect(flyout.locator('[data-testid="start-menu-flyout-empty"]')).toHaveText('No recent files');
    await expect.poll(() => (JSON.parse(readFileSync(stateFile(router), 'utf8')) as { recent: unknown[] }).recent.length).toBe(0);
  });

  test('Ctrl+Space search matches recent paths and opens them', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'todo.txt', 'buy milk');

    await page.keyboard.press('Control+Space');
    const input = page.locator('[data-testid="palette-input"]');
    await expect(input).toBeFocused();
    // The query matches anywhere in the path — the directory works too.
    await input.fill('todo');
    const row = page.locator('[data-testid="palette-list"] [data-path$="/todo.txt"]');
    await expect(row).toBeVisible();
    await expect(row).toContainText('todo.txt');
    // No app matches "todo", so the recent row is the only (selected) result.
    await expect(page.locator('[data-testid="palette-list"] button')).toHaveCount(1);

    const from = router.logCursor();
    await page.keyboard.press('Enter');
    await expect(page.locator('[data-testid="palette"]')).toHaveCount(0);
    await router.waitForLog(/open\.request: path="[^"]*\/todo\.txt" handler=com\.wash\.edit from=com\.wash\.session/, 10_000, from);
    await expect(page.locator('wash-app-edit')).toHaveCount(2, { timeout: 15_000 });
  });

  test('a recent file that no longer exists is not listed (but stays on disk)', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'todo.txt', 'buy milk');
    await expect.poll(() => existsSync(stateFile(router))).toBe(true);

    // Delete it behind everyone's back, then force a fresh launcher.state by
    // reloading the shell (desktop.request re-ships the launcher state).
    const { unlinkSync } = await import('node:fs');
    unlinkSync(join(router.fmRoot, 'todo.txt'));
    await page.reload();
    await expect(page.locator('wash-app-session')).toBeVisible();
    const { menu, flyout } = await openEditFlyout(page);
    await expect(menu.getByRole('button', { name: /^Files$/ })).toBeVisible();
    await expect(flyout.locator('[data-testid="start-menu-flyout-item"]')).toHaveCount(0);
    await expect(flyout.locator('[data-testid="start-menu-flyout-empty"]')).toBeVisible();
    // Read-side filter only: the record is still on disk for when the file returns.
    const st = JSON.parse(readFileSync(stateFile(router), 'utf8')) as { recent: { path: string }[] };
    expect(st.recent.map((r) => r.path)).toEqual([join(router.fmRoot, 'todo.txt')]);
  });

  test('typing in the start menu flattens Recent into matching files', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'todo.txt', 'buy milk');

    await page.locator('button[title="Apps"]').click();
    const menu = page.locator('[data-testid="start-menu"]');
    await menu.locator('[data-testid="start-menu-search"]').fill('todo');
    // No flyout rows while searching — the matching file itself is listed.
    await expect(menu.locator('[data-testid="start-menu-recent-group-edit"]')).toHaveCount(0);
    const hit = menu.locator('[data-testid="start-menu-recent-item"]');
    await expect(hit).toHaveCount(1);
    await expect(hit).toContainText('todo.txt');
    const from = router.logCursor();
    await page.keyboard.press('Enter');
    await router.waitForLog(/open\.request: path="[^"]*\/todo\.txt" handler=com\.wash\.edit from=com\.wash\.session/, 10_000, from);
  });

  test('the Edit flyout is keyboard-driven: Right opens, arrows move, Left closes, Enter opens', async ({ page, router }) => {
    await openFm(page, router);
    await openViaFm(page, router, 'notes.md', 'hello from the editor');
    await openViaFm(page, router, 'todo.txt', 'buy milk');

    await page.locator('button[title="Apps"]').click();
    const menu = page.locator('[data-testid="start-menu"]');
    await expect(menu.locator('[data-testid="start-menu-search"]')).toBeFocused();
    // Walk to the Edit row: nothing is pinned, so Recent starts the list
    // (Files, Edit, …). The first arrow only reveals the cursor.
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await expect(menu.locator('[data-selected="true"]')).toContainText('Edit');
    await page.keyboard.press('ArrowRight');
    const flyout = page.locator('[data-testid="start-menu-flyout"]');
    await expect(flyout).toBeVisible();
    await expect(flyout.locator('[data-selected="true"]')).toContainText('todo.txt');
    await page.keyboard.press('ArrowDown');
    await expect(flyout.locator('[data-selected="true"]')).toContainText('notes.md');
    // Left closes only the flyout.
    await page.keyboard.press('ArrowLeft');
    await expect(flyout).toHaveCount(0);
    await expect(menu).toBeVisible();
    await page.keyboard.press('Enter');
    await expect(flyout).toBeVisible();
    const from = router.logCursor();
    await page.keyboard.press('Enter');
    await expect(menu).toHaveCount(0);
    await router.waitForLog(/open\.request: path="[^"]*\/todo\.txt" handler=com\.wash\.edit from=com\.wash\.session/, 10_000, from);
  });
});
