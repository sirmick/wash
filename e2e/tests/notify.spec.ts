// Notification ergonomics, full-stack:
//
//   app c.Info/c.Fail (internal/sdk/outbound.go)
//     → router relay → com.wash.notify service (history)
//     → router ShellNotify broadcast → shell toast (web/shell/src/notify.ts)
//     → session NotifyWidget history row (apps/session/fe sidebar)
//
// Per [[wash e2e pattern]] we drive a real user action (a bulk delete
// through fm, which lands on wash-bulk's terminal-state toast) and
// assert both the transient toast and the persisted sidebar row. The
// router-log signal (status=done) is the BE-side proof the op finished.

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-path"]')).toHaveValue(router.fmRoot);
}

test('bulk delete fires a "Delete complete" toast + records it in the sidebar', async ({ page, router }) => {
  writeFileSync(join(router.fmRoot, 'n-a.txt'), 'a');
  writeFileSync(join(router.fmRoot, 'n-b.txt'), 'b');
  await openFm(page, router);

  // Multi-select both, delete via context menu, confirm.
  await page.locator('[data-testid="fm-entry-n-a.txt"]').click();
  await page.locator('[data-testid="fm-entry-n-b.txt"]').click({ modifiers: ['Control'] });
  await page.locator('[data-testid="fm-entry-n-a.txt"]').click({ button: 'right' });
  await page.locator('[data-testid="fm-ctx-delete"]').click();
  await page.locator('[data-testid="fm-confirm-delete-yes"]').click();

  // BE-side proof the job finished.
  await router.waitForLog(/bulk-ops job=\S+ op=delete status=done/, 5_000);

  // The transient shell toast (info level) carries the op summary.
  const toast = page.locator('[data-testid="notification"][data-level="info"]').last();
  await expect(toast.locator('[data-testid="notification-title"]')).toHaveText('Delete complete', { timeout: 5_000 });
  await expect(toast.locator('[data-testid="notification-body"]')).toContainText('2 items');

  // The notify service persisted it: a history row in the sidebar widget.
  await expect(page.locator('[data-testid="notify-widget"]')).toContainText('Delete complete', { timeout: 5_000 });
});

test('a failed op fires an error toast', async ({ page, router }) => {
  // Enqueue a copy onto a non-writable destination via the bulk wire so
  // the job reaches status=failed, exercising the c.Fail error path.
  await openFm(page, router);
  const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.bulk' });
  const inst = launched.instance_id as string;

  const ack = await router.sendAppMsg(inst, {
    kind: 'enqueue', id: 'nf-1', op: 'copy',
    paths: [join(router.fmRoot, 'does-not-exist.txt')],
    dest: '/proc/wash-nonexistent-dest', // unwritable → the job fails
  });
  expect(ack.kind).toBe('enqueue_ok');

  await router.waitForLog(/bulk-ops job=\S+ op=copy status=failed/, 5_000);

  const toast = page.locator('[data-testid="notification"][data-level="error"]').last();
  await expect(toast.locator('[data-testid="notification-title"]')).toHaveText('Copy failed', { timeout: 5_000 });
});
