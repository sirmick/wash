import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

test.use({
  routerOpts: {
    apps: ['session', 'fm', 'edit', 'term'],
    fmRoot: true,
    fmSeed(root) {
      mkdirSync(join(root, 'sub folder'));
      writeFileSync(join(root, 'top.txt'), 'top level\n');
      writeFileSync(join(root, 'sub folder', 'note.txt'), 'nested shortcut fixture\n');
    },
  },
});

async function openApp(page: Page, url: string, app: 'edit' | 'fm') {
  await page.goto(url);
  await page.locator('button[title="Apps"]').click();
  await page.getByTestId('start-menu').getByRole('button', { name: app === 'edit' ? 'Editor' : 'Files', exact: true }).click();
  const win = page.locator(`wash-app-${app}`);
  await expect(win.getByTestId(`${app}-entry-sub folder`)).toBeVisible();
  return win;
}

async function terminalIn(page: Page, dir: string) {
  const host = page.locator('wash-app-term').getByTestId('term-host').first();
  await expect(host).toBeVisible();
  const buffer = () => host.evaluate((el: any) => {
    const buf = el.__washTerm?.buffer.active;
    if (!buf) return '';
    return Array.from({ length: buf.length }, (_, y) => buf.getLine(y)?.translateToString(true) ?? '').join('\n');
  });
  await expect.poll(buffer).toMatch(/[$#%>][ ]?/);
  await host.click();
  const quoted = `'${dir.replace(/'/g, "'\\''")}'`;
  await page.keyboard.type(`test "$PWD" = ${quoted} && echo WASH_CWD_OK`);
  await page.keyboard.press('Enter');
  await expect.poll(buffer).toMatch(/^WASH_CWD_OK$/m);
}

async function editorFolder(editor: Locator, dir: string) {
  await expect(editor.getByTestId('edit-sidebar')).toContainText(dir);
  await expect(editor.getByTestId('edit-entry-note.txt')).toBeVisible();
  await expect(editor.getByTestId('edit-entry-top.txt')).toHaveCount(0);
}

test.describe('folder shortcuts', () => {
  test.setTimeout(45_000);

  for (const target of ['terminal', 'file-manager'] as const) {
    test(`Editor File menu opens ${target} at project root despite a nested active file`, async ({ page, router }) => {
      const editor = await openApp(page, router.url, 'edit');
      await editor.getByTestId('edit-entry-sub folder').dblclick();
      await editor.getByTestId('edit-entry-note.txt').dblclick();
      await expect(editor.locator('.cm-content')).toContainText('nested shortcut fixture');
      await editor.getByTestId('edit-menubar-file').click();
      const action = page.getByTestId(`edit-menu-open-${target}`);
      await expect(action).toHaveAttribute('title', router.fmRoot);
      await action.click();
      if (target === 'terminal') await terminalIn(page, router.fmRoot);
      else await expect(page.locator('wash-app-fm').getByTestId('fm-path')).toHaveValue(router.fmRoot);
    });

    for (const row of ['folder', 'file'] as const) {
      test(`Editor ${row} context opens ${target} at the clicked folder`, async ({ page, router }) => {
        const editor = await openApp(page, router.url, 'edit');
        if (row === 'file') await editor.getByTestId('edit-entry-sub folder').dblclick();
        await editor.getByTestId(`edit-entry-${row === 'folder' ? 'sub folder' : 'note.txt'}`).click({ button: 'right' });
        const action = page.getByTestId(`edit-ctx-open-${target}`);
        const dir = join(router.fmRoot, 'sub folder');
        await expect(action).toHaveAttribute('title', dir);
        await action.click();
        if (target === 'terminal') await terminalIn(page, dir);
        else await expect(page.locator('wash-app-fm').getByTestId('fm-path')).toHaveValue(dir);
      });
    }
  }

  test('File Manager toolbar opens the viewed folder as an Editor project', async ({ page, router }) => {
    const fm = await openApp(page, router.url, 'fm');
    const dir = join(router.fmRoot, 'sub folder');
    await fm.getByTestId('fm-path').fill(dir);
    await fm.getByTestId('fm-path').press('Enter');
    await expect(fm.getByTestId('fm-entry-note.txt')).toBeVisible();
    const action = fm.getByTestId('fm-open-text-editor');
    await expect(action).toHaveAttribute('title', `Open text editor in this folder: ${dir}`);
    await action.click();
    await editorFolder(page.locator('wash-app-edit'), dir);
  });

  test('File Manager folder context opens that folder as an Editor project', async ({ page, router }) => {
    const fm = await openApp(page, router.url, 'fm');
    await fm.getByTestId('fm-entry-sub folder').click({ button: 'right' });
    const dir = join(router.fmRoot, 'sub folder');
    const action = page.getByTestId('fm-ctx-open-text-editor');
    await expect(action).toHaveAttribute('title', dir);
    await action.click();
    await editorFolder(page.locator('wash-app-edit'), dir);
  });

  test('File Manager file context opens the actual file in an Editor tab', async ({ page, router }) => {
    const fm = await openApp(page, router.url, 'fm');
    await fm.getByTestId('fm-entry-sub folder').dblclick();
    await fm.getByTestId('fm-entry-note.txt').click({ button: 'right' });
    const action = page.getByTestId('fm-ctx-open-text-editor');
    await expect(action).toHaveText('Open in text editor');
    await expect(action).toHaveAttribute('title', join(router.fmRoot, 'sub folder', 'note.txt'));
    await action.click();
    await expect(page.locator('wash-app-edit .cm-content')).toContainText('nested shortcut fixture');
  });
});
