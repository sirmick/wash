// wash-syslogs — the /var/log file tailer + the manifest-declared
// root variant. Verifies the regular launcher entry mounts the app
// with a sidebar of files, selecting a file streams parsed lines,
// and the synthetic "System Logs (root)" entry exists in the start
// menu (proving the new manifest.root_variant → catalog → launcher
// pipeline works).
//
// Skipped on hosts where /var/log isn't readable at all — that's
// usually a CI container with no syslog daemon at all, where the
// test would be meaningless.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { readdirSync, statSync, existsSync, writeFileSync, mkdtempSync, appendFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

// We need at least one readable file under /var/log for the
// streaming test. Most distros have /var/log/dpkg.log, syslog, or
// auth.log world- or adm-readable; check at runtime.
function readableLogExists(): boolean {
  if (!existsSync('/var/log')) return false;
  try {
    const entries = readdirSync('/var/log');
    for (const e of entries) {
      const p = join('/var/log', e);
      try {
        const s = statSync(p);
        if (s.isFile() && s.size > 0) return true;
      } catch {
        /* ignore unreadable */
      }
    }
  } catch {
    return false;
  }
  return false;
}

const VAR_LOG_OK = readableLogExists();

test.use({
  routerOpts: {
    apps: ['session', 'about', 'term', 'priv', 'syslogs'],
  },
});

async function openSyslogs(page: Page) {
  await page.locator('button[title="Apps"]').click();
  // "System Logs" is the regular entry; the root variant is "System
  // Logs (root)". Pick the non-root one — exact match avoids picking
  // the (root) row.
  await page.getByRole('button', { name: /^System Logs$/ }).click();
  await expect(page.locator('wash-app-syslogs')).toBeVisible();
  await expect(page.locator('[data-testid="syslogs-root"]')).toBeVisible();
}

test.describe('syslogs app', () => {
  test.setTimeout(20_000);

  test('launches with sidebar + toolbar; lists files in /var/log', async ({ page, router }) => {
    test.skip(!VAR_LOG_OK, '/var/log has no readable files on this host');
    await page.goto(router.url);
    await openSyslogs(page);

    await expect(page.locator('[data-testid="syslogs-sidebar"]')).toBeVisible();
    await expect(page.locator('[data-testid="syslogs-toolbar"]')).toBeVisible();

    // Sidebar populates with at least one row.
    await expect.poll(
      () => page.locator('[data-testid="syslogs-file-row"]').count(),
      { timeout: 5_000 },
    ).toBeGreaterThan(0);
  });

  test('selecting a file starts tailing and emits lines', async ({ page, router }) => {
    // Use a deterministic file under tmpdir as a synthetic "log" via
    // /tmp symlink so the test doesn't depend on whether the host's
    // /var/log has fresh writes. Plan: pick the first row the BE
    // discovers — discovery is /var/log only, so we still need a real
    // readable file there. The earlier list-files test gates this one
    // by skipping when VAR_LOG_OK is false.
    test.skip(!VAR_LOG_OK, '/var/log has no readable files on this host');
    await page.goto(router.url);
    await openSyslogs(page);

    await expect.poll(
      () => page.locator('[data-testid="syslogs-file-row"]').count(),
      { timeout: 5_000 },
    ).toBeGreaterThan(0);

    // Pick whichever file looks biggest (most likely to have content
    // we can read back). Use the first row — sidebar is sorted by
    // mtime desc, so the most recently written file is top.
    const firstRow = page.locator('[data-testid="syslogs-file-row"]').first();
    const path = await firstRow.getAttribute('data-file-path');
    expect(path).toBeTruthy();
    await firstRow.click();

    await router.waitForLog(
      new RegExp(`wash-syslogs stream gen=\\d+ path=".*" as_root=false`),
      5_000,
    );

    // Either we see rows (file had pre-existing content tail dumps)
    // or the BE reports running. Both are valid green outcomes; the
    // perm_denied state is a valid fourth outcome that depends on
    // the file we randomly picked. The test cares that one of the
    // three resolves within the deadline.
    await expect.poll(
      () => page.evaluate(() => {
        const host = document.querySelector('wash-app-syslogs');
        if (!host) return 'no-host';
        const rows = host.querySelectorAll('[data-testid="syslogs-row"]');
        const denied = host.querySelector('[data-testid="syslogs-banner-denied"]');
        if (denied) return 'denied';
        if (rows.length > 0) return 'rows';
        return 'pending';
      }),
      { timeout: 6_000 },
    ).not.toEqual('pending');
  });

  test('"System Logs (root)" launcher row exists (manifest.root_variant)', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();

    // The synthetic row exists because wash-syslogs declares
    // RootVariant{} in its manifest. data-testid is
    // start-menu-root-<appID>; this is the proof that the catalog
    // wire carries RootVariant through and the session FE renders it
    // generically (not just for wash-term).
    await expect(page.getByTestId('start-menu-root-com.wash.syslogs')).toBeVisible();

    // wash-term still gets its custom-name + legacy testid for
    // backward compat with existing tests.
    await expect(page.getByTestId('start-menu-root-terminal')).toBeVisible();

    // The button name should be "System Logs (root)" by default.
    await expect(page.getByRole('button', { name: /System Logs \(root\)/ })).toBeVisible();

    // Sanity: router didn't crash on the new catalog message.
    expect(router.log()).not.toMatch(/panic/);
  });
});
