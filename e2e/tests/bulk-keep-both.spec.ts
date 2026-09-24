// Keep Both answers a copy collision by renaming the incoming item, so
// neither side is lost. bulkops has carried the action since Duplicate
// landed; until now the dialog had no button for it, so the only answers
// on offer destroyed one side or skipped it.
//
// Both halves: the button resolves the conflict, and the disk afterwards
// holds BOTH files with the original's bytes intact.

import { test, expect } from '../fixtures/router';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { apps: ['session', 'fm', 'bulk', 'notify'], fmRoot: true } });

test('Keep Both renames the incoming file instead of replacing it', async ({ page, router }) => {
  test.setTimeout(30_000);
  mkdirSync(join(router.fmRoot, 'src'), { recursive: true });
  writeFileSync(join(router.fmRoot, 'src', 'clash.txt'), 'incoming');
  writeFileSync(join(router.fmRoot, 'clash.txt'), 'existing');

  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();

  const bulk = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  await router.sendAppMsg(String(bulk.instance_id), {
    kind: 'enqueue', id: 'kb-1', op: 'copy',
    paths: [join(router.fmRoot, 'src', 'clash.txt')],
    dest: router.fmRoot,
  });

  const keep = page.locator('[data-testid^="bulk-conflict-keep-both-"]').first();
  await expect(keep).toBeVisible({ timeout: 20_000 });
  await keep.click();

  await router.waitForLog(/bulk-ops job=\S+ op=copy status=done/, 15_000);
  // Both survive: the original untouched, the incoming one renamed.
  expect(readFileSync(join(router.fmRoot, 'clash.txt'), 'utf8')).toBe('existing');
  expect(existsSync(join(router.fmRoot, 'clash (copy).txt'))).toBe(true);
  expect(readFileSync(join(router.fmRoot, 'clash (copy).txt'), 'utf8')).toBe('incoming');
});
