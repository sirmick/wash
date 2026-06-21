// fm cross-origin DnD guard: a drag from a different shell origin (a remote
// fm window over wash-remote) carries paths in THAT host's filesystem, which
// this window's router can't move — it'd rename the wrong file or fail. The
// drop must be rejected, not attempted. A real two-host setup isn't available
// here, so we synthesize the drop with a DataTransfer tagged with a foreign
// origin (application/x-wash-origin). See docs/REMOTE.md (deferred non-goal).

import { test, expect, seedSimpleTree } from '../fixtures/router';
import { existsSync } from 'node:fs';
import { join } from 'node:path';

test.use({ routerOpts: { apps: ['session', 'about', 'fm'], fmRoot: true, fmSeed: seedSimpleTree } });

async function openFm(
  page: import('@playwright/test').Page,
  router: import('../fixtures/router').RouterHandle,
) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
  await expect(page.locator('wash-app-fm')).toBeVisible();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

test('a cross-origin (remote) drop onto a folder is rejected, not moved', async ({ page, router }) => {
  await openFm(page, router);
  const src = join(router.fmRoot, 'hello.txt');

  // Synthesize a drop on docs/ carrying hello.txt tagged with a FOREIGN origin
  // (the local window's origin is 'local'). The DRAG_MIME/ORIGIN_MIME strings
  // mirror @wash/fs-client/dnd.ts.
  const dispatched = await page.evaluate((srcPath) => {
    const row = document.querySelector('wash-app-fm [data-testid="fm-entry-docs"]') as HTMLElement | null;
    if (!row) return false;
    const dt = new DataTransfer();
    dt.setData('application/x-wash-paths', JSON.stringify([srcPath]));
    dt.setData('application/x-wash-origin', 'remote-host-B');
    row.dispatchEvent(new DragEvent('dragover', { dataTransfer: dt, bubbles: true, cancelable: true }));
    row.dispatchEvent(new DragEvent('drop', { dataTransfer: dt, bubbles: true, cancelable: true }));
    return true;
  }, src);
  expect(dispatched).toBe(true);

  // The status bar flashes the rejection (proves the guard ran)…
  await expect(page.locator('wash-app-fm [data-testid="fm-status"]')).toContainText(/between machines/i, { timeout: 3_000 });
  // …and nothing moved.
  expect(existsSync(src)).toBe(true);
  expect(existsSync(join(router.fmRoot, 'docs', 'hello.txt'))).toBe(false);
});
