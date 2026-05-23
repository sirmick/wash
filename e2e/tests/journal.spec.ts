// wash-journal — sidebar + log pane fed by journalctl. Verifies the
// system stream auto-starts, the unit list populates, selecting a
// unit re-spawns journalctl with -u, the text filter narrows visible
// rows client-side, and clicking the "Read with root" banner emits a
// privileged select. Privilege completion isn't exercised here — the
// priv-side handshake has its own coverage in priv.spec.ts.
//
// Skipped automatically when journalctl is missing — the wash-journal
// BE just won't produce lines and the suite is meaningless. The
// kernel-tracked journal-group permission gives the test runner read
// access to the system log on every reasonable distro, so we don't
// gate on uid 0.

import { test, expect } from '../fixtures/router';
import type { Page } from '@playwright/test';
import { existsSync } from 'node:fs';

const HAS_JOURNALCTL = existsSync('/usr/bin/journalctl') || existsSync('/bin/journalctl');

test.skip(!HAS_JOURNALCTL, 'journalctl not installed');

test.use({
  routerOpts: {
    apps: ['session', 'about', 'term', 'priv', 'journal'],
  },
});

async function openJournal(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page.getByRole('button', { name: /Journal/ }).click();
  await expect(page.locator('wash-app-journal')).toBeVisible();
  await expect(page.locator('[data-testid="journal-root"]')).toBeVisible();
}

// Some test environments yield zero entries for the "boot" view if
// the box just rebooted; switch to "all" so the test isn't racing the
// journal's own write cadence.
async function pickAll(page: Page) {
  await page.locator('[data-testid="journal-toolbar"]').getByRole('button', { name: 'all' }).first().click();
}

test.describe('journal app', () => {
  test.setTimeout(20_000);

  test('launches with sidebar + toolbar + system row', async ({ page, router }) => {
    await page.goto(router.url);
    await openJournal(page);

    await expect(page.locator('[data-testid="journal-sidebar"]')).toBeVisible();
    await expect(page.locator('[data-testid="journal-toolbar"]')).toBeVisible();
    await expect(page.locator('[data-testid="journal-system-row"]')).toBeVisible();

    // Units list populates within a couple of seconds (one
    // systemctl call). A box with no services at all would be
    // suspicious — every distro the test runs on has dozens.
    await expect.poll(
      () => page.locator('[data-testid="journal-unit-row"]').count(),
      { timeout: 5_000 },
    ).toBeGreaterThan(0);

    // BE logged the initial stream spawn.
    await router.waitForLog(/wash-journal stream gen=\d+ unit="" range=boot/, 5_000);
  });

  test('system view paints log entries', async ({ page, router }) => {
    await page.goto(router.url);
    await openJournal(page);
    await pickAll(page);

    // At least one row eventually appears. We don't pin a number —
    // a quiet box may take a few seconds to emit anything, so the
    // poll has a generous deadline.
    await expect.poll(
      () => page.locator('[data-testid="journal-row"]').count(),
      { timeout: 10_000 },
    ).toBeGreaterThan(0);

    expect(router.log()).toContain('wash-journal stream gen=');
  });

  test('selecting a unit re-spawns journalctl with -u', async ({ page, router }) => {
    await page.goto(router.url);
    await openJournal(page);

    // Wait for the unit list to settle.
    await expect.poll(
      () => page.locator('[data-testid="journal-unit-row"]').count(),
      { timeout: 5_000 },
    ).toBeGreaterThan(0);

    // Pick the first unit row and capture its name.
    const firstUnit = page.locator('[data-testid="journal-unit-row"]').first();
    const name = await firstUnit.getAttribute('data-unit-name');
    expect(name).toBeTruthy();

    // Click it. The BE logs a fresh stream for that unit name.
    await firstUnit.click();
    await router.waitForLog(
      new RegExp(`wash-journal stream gen=\\d+ unit="${escapeRegex(name!)}" range=boot`),
      5_000,
    );
  });

  test('text filter narrows visible rows', async ({ page, router }) => {
    await page.goto(router.url);
    await openJournal(page);
    await pickAll(page);

    await expect.poll(
      () => page.locator('[data-testid="journal-row"]').count(),
      { timeout: 10_000 },
    ).toBeGreaterThan(0);
    const total = await page.locator('[data-testid="journal-row"]').count();

    // A 24-char random ASCII filter will almost certainly match
    // zero rows. We don't need exactly 0 — just strictly fewer
    // than the unfiltered set.
    await page.locator('[data-testid="journal-filter"]').fill('zzzz-no-such-message-zzzz');
    await expect.poll(
      () => page.locator('[data-testid="journal-row"]').count(),
    ).toBeLessThan(total);

    // Clearing the filter restores the row count (or grows it,
    // since the live stream keeps appending).
    await page.locator('[data-testid="journal-filter"]').fill('');
    await expect.poll(
      () => page.locator('[data-testid="journal-row"]').count(),
    ).toBeGreaterThanOrEqual(total);

    // Sanity: router still happy.
    expect(router.log()).not.toMatch(/panic/);
  });

  test('perm-denied banner → Read with root → priv-side select', async ({ page, router }) => {
    await page.goto(router.url);
    await openJournal(page);

    // Inject a stream_state perm_denied directly — the unprivileged
    // journal path doesn't normally fail on a logged-in user that's
    // in the systemd-journal group, so we synthesize the BE event
    // the FE would see in the failing case. The retry button is
    // wired to the real BE.
    await page.evaluate(() => {
      const host = document.querySelector('wash-app-journal');
      if (!host) throw new Error('wash-app-journal element missing');
      host.dispatchEvent(
        new CustomEvent('wash:msg', {
          detail: {
            kind: 'stream_state',
            gen: 1,
            status: 'perm_denied',
            error: 'mocked: permission denied',
            as_root: false,
          },
        }),
      );
    });

    await expect(page.locator('[data-testid="journal-banner-denied"]')).toBeVisible();
    await page.locator('[data-testid="journal-retry-root"]').click();

    // Clicking the retry button sends a select with as_root=true.
    // The BE logs that, then attempts to invoke wash-priv (which
    // won't complete the request without an unlocked password — we
    // only assert the wash-journal side issued the privileged call).
    await router.waitForLog(/wash-journal stream gen=\d+ .*as_root=true/, 5_000);
  });
});

// escapeRegex — small local helper. The unit name we substitute into
// a RegExp can legitimately contain `.` (in `foo.service`), which
// matches any character; we want a literal match against the BE log.
function escapeRegex(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
