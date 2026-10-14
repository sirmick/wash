// edit's sidebar Download: the same egress as fm's (internal/download) —
// right-click a row, the BE streams the file (a folder as a zip) over a
// raw channel, and the browser saves it. Asserted on Playwright's download
// event, so the bytes are proven to have reached the browser.

import { test, expect } from '../fixtures/router';
import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

const BODY = 'download me from the editor\n';

function seed(root: string): void {
  writeFileSync(join(root, 'plain.txt'), BODY);
  mkdirSync(join(root, 'sub'));
  writeFileSync(join(root, 'sub', 'inside.txt'), 'inside\n');
}

test.use({ routerOpts: { fmRoot: true, fmSeed: seed } });

async function openEditor(page: import('@playwright/test').Page, url: string) {
  await page.goto(url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /Editor/ }).click();
  const editor = page.locator('wash-app-edit');
  await expect(editor.locator('[data-testid="edit-entry-plain.txt"]')).toBeVisible();
  return editor;
}

test('a file downloads with its exact bytes', async ({ page, router }) => {
  const editor = await openEditor(page, router.url);
  await editor.locator('[data-testid="edit-entry-plain.txt"]').click({ button: 'right' });
  await expect(page.locator('[data-testid="edit-ctx-download"]')).toHaveText(/^Download$/);
  const downloadPromise = page.waitForEvent('download');
  await page.locator('[data-testid="edit-ctx-download"]').click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('plain.txt');
  expect(readFileSync((await download.path())!, 'utf8')).toBe(BODY);
  await router.waitForLog(/wash-edit download id=\S+ status=done file="plain.txt" zip=false/, 5_000);
});

test('a folder downloads as a zip', async ({ page, router }) => {
  const editor = await openEditor(page, router.url);
  await editor.locator('[data-testid="edit-entry-sub"]').click({ button: 'right' });
  await expect(page.locator('[data-testid="edit-ctx-download"]')).toHaveText(/Download as zip/);
  const downloadPromise = page.waitForEvent('download');
  await page.locator('[data-testid="edit-ctx-download"]').click();
  const download = await downloadPromise;
  expect(download.suggestedFilename()).toBe('sub.zip');
  expect(readFileSync((await download.path())!).subarray(0, 2).toString('latin1')).toBe('PK');
});
