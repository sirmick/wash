// The Agent window's editor, and the files its transcript names.
//
// Each Agent window keeps ONE wash-edit window of its own, rooted at the
// session's folder, like the editor beside an IDE's agent pane: the Editor
// button opens it the first time and brings it forward after, and a file
// the agent names — a tool row, or `notes.md:2` in its prose — opens there,
// at its line. Only files at or below the session's folder are links.
//
// The same links work in wash-edit's own agent tabs, where the file opens
// in the buffer above the transcript.

import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, closeButtonOf, startAgentSession } from '../fixtures/agents';

// The project is the sandbox root, so it is where the editor opens and the
// session works: the files the fake's `mentionfiles` reply names, plus a
// dotfile for the hidden-files toggle.
function project(dir: string): void {
  mkdirSync(join(dir, 'src'));
  writeFileSync(join(dir, 'notes.md'), '# notes\nsecond line\n');
  writeFileSync(join(dir, 'src', 'app.go'), 'package app\n');
  writeFileSync(join(dir, '.env'), 'A=1\n');
}

test.use({
  routerOpts: {
    apps: [...AGENT_APPS, 'edit'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
    fmSeed: project,
  },
});

// raise brings the Agent window back in front of its editor, as clicking it
// would: the editor coming forward is the point, and it then covers what
// the next click is aimed at.
async function raise(win: Locator) {
  const id = Number(await win.getAttribute('data-wash-window'));
  await win.page().evaluate((w) => window.wash.focusWindow(w), id);
}

async function prompt(win: Locator, text: string) {
  const composer = win.locator('textarea');
  await composer.fill(text);
  await composer.press('Enter');
}

test.describe('the Agent window and its editor', () => {
  test.setTimeout(90_000);

  test('one editor per Agent window; the files its agent names open there, at their line', async ({ page, router }) => {
    const dir = router.fmRoot;
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const win = await startAgentSession(page, undefined, { cwd: dir });
    const editors = page.locator('wash-app-edit');

    // The button opens an editor on the session's folder…
    const button = win.locator('[data-testid="ai-show-editor"]');
    await expect(button).toBeEnabled({ timeout: 20_000 });
    await button.click();
    await expect(editors).toHaveCount(1, { timeout: 20_000 });
    const edit = editors.first();
    await expect(edit.locator('[data-testid="edit-sidebar"]')).toContainText(dir);
    await expect(edit.locator('[data-testid="edit-entry-notes.md"]')).toBeVisible();

    // …and after that brings the same one forward.
    await raise(win);
    await button.click();
    await raise(win);
    await prompt(win, 'mentionfiles');

    // The two files under the folder are links; /etc/hosts is not.
    const links = win.locator('[data-testid="agent-path-link"]');
    await expect(links).toHaveText(['notes.md:2', 'src/app.go'], { timeout: 20_000 });

    // notes.md:2 opens in that editor — in source, a line being a place in
    // the source — with the caret on line 2.
    await links.first().click();
    await expect(edit.locator(`[data-testid="edit-tab-${join(dir, 'notes.md')}"]`)).toBeVisible();
    await expect(edit.locator('[data-testid="edit-status-cursor"]')).toHaveText(/Ln 2, Col 1/);
    await raise(win);
    await links.nth(1).click();
    await expect(edit.locator(`[data-testid="edit-tab-${join(dir, 'src', 'app.go')}"]`)).toBeVisible();
    await expect(editors).toHaveCount(1);

    // Close it, and the next link opens a new one rather than talking to
    // nobody.
    await closeButtonOf(page, edit).click();
    await expect(editors).toHaveCount(0);
    await raise(win);
    await links.first().click();
    await expect(editors).toHaveCount(1, { timeout: 20_000 });
    await expect(editors.first().locator(`[data-testid="edit-tab-${join(dir, 'notes.md')}"]`)).toBeVisible();
  });

  test("an editor agent tab's links open in the buffer above it", async ({ page, router }) => {
    const dir = router.fmRoot;
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.edit' });
    const edit = page.locator('wash-app-edit').first();
    await expect(edit.locator('[data-testid="edit-entry-notes.md"]')).toBeVisible({ timeout: 20_000 });

    await edit.getByRole('button', { name: 'Terminal', exact: true }).click();
    await page.locator('[data-testid="edit-menu-agent-codex"]').click();
    const pane = edit.locator('[data-testid="edit-term-pane"]');
    const composer = pane.locator('[data-testid="agent-composer"]');
    await expect(composer).toBeVisible({ timeout: 30_000 });
    await composer.fill('mentionfiles');
    await composer.press('Enter');

    const links = pane.locator('[data-testid="agent-path-link"]');
    await expect(links).toHaveText(['notes.md:2', 'src/app.go'], { timeout: 30_000 });
    await links.first().click();
    await expect(edit.locator(`[data-testid="edit-tab-${join(dir, 'notes.md')}"]`)).toBeVisible();
    await expect(edit.locator('[data-testid="edit-status-cursor"]')).toHaveText(/Ln 2, Col 1/);
  });

  test('the editor shows hidden files when asked, and remembers', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.edit' });
    const edit = page.locator('wash-app-edit').first();
    await expect(edit.locator('[data-testid="edit-entry-notes.md"]')).toBeVisible({ timeout: 20_000 });
    await expect(edit.locator('[data-testid="edit-entry-.env"]')).toHaveCount(0);

    await edit.getByRole('button', { name: 'View', exact: true }).click();
    await page.locator('[data-testid="edit-menu-show-hidden"]').click();
    await expect(edit.locator('[data-testid="edit-entry-.env"]')).toBeVisible();

    // Per window, across a reload.
    await page.waitForTimeout(500); // persist() is debounced 250ms
    await page.reload();
    await expect(page.locator('wash-app-edit').first().locator('[data-testid="edit-entry-.env"]')).toBeVisible({ timeout: 20_000 });
  });
});
