// The fm selection invariant (apps/fm/fe/src/main.tsx) is an observe-only,
// always-on check: every selected path must be a currently-VISIBLE row.
// `selection` is a free-floating set nothing reconciles against the
// listing, so a change underneath a live selection can leave "ghost"
// paths selected. The check logs (console.error) but never prunes, so we
// can catch the divergence in the wild without masking its trigger.
//
// These tests pin the instrumentation: a deterministic trigger (collapse
// a folder whose child is selected → the child leaves the visible list)
// must log, and ordinary selection must NOT (no false positives).

import { test, expect, seedSimpleTree } from '../fixtures/router';

test.use({ routerOpts: { fmRoot: true, fmSeed: seedSimpleTree } });

function captureViolations(page: import('@playwright/test').Page): string[] {
  const out: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error' && msg.text().includes('[fm] selection invariant')) out.push(msg.text());
  });
  return out;
}

async function openFm(page: import('@playwright/test').Page, router: import('../fixtures/router').RouterHandle) {
  await page.goto(router.url);
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Files/ }).click();
  await expect(page.locator('[data-testid="fm-entry-hello.txt"]')).toBeVisible();
}

test('logs when a selected row leaves the visible list (collapse its parent)', async ({ page, router }) => {
  const violations = captureViolations(page);
  await openFm(page, router);

  // Expand docs and select its child — still visible, so no violation.
  await page.locator('[data-testid="fm-entry-docs"]').dblclick();
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toBeVisible();
  await page.locator('[data-testid="fm-entry-readme.md"]').click();
  await page.waitForTimeout(100);
  expect(violations).toHaveLength(0);

  // Collapse docs via its chevron → readme.md leaves visibleRows but
  // stays in the selection → the strict invariant fires.
  await page.locator('[data-testid="fm-chevron-docs"]').click();
  await expect(page.locator('[data-testid="fm-entry-readme.md"]')).toHaveCount(0);

  await expect.poll(() => violations.length, { timeout: 3000 }).toBeGreaterThan(0);
  expect(violations[0]).toContain('not in the visible list');
});

test('no false positive: plain, ctrl, and select-all selections log nothing', async ({ page, router }) => {
  const violations = captureViolations(page);
  await openFm(page, router);

  await page.locator('[data-testid="fm-entry-hello.txt"]').click();
  await page.locator('[data-testid="fm-entry-binary.bin"]').click({ modifiers: ['Control'] });
  await page.keyboard.press('Control+a');
  await page.locator('[data-testid="fm-list"]').click({ position: { x: 40, y: 360 } }); // background-clear
  await page.waitForTimeout(150);

  expect(violations).toHaveLength(0);
});
