// End session ends everything the session owns (docs/Review-findings.md,
// 2026-09-08 sweep, "End session leaves agent-spawned terminals running
// and asks pending").
//
// retire() used to kill the adapter process and nothing else: a terminal
// the agent had created through terminal/create kept running with its
// channel mounted in the transcript, and a permission question it was
// blocked on stayed on every rail — "claude wants to run…" for a session
// that no longer existed, with "Always allow" still writing a rule for
// it. Both halves are asserted here: the rail/roster (FE) and agentd's
// own log of what it closed (BE).

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function openAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
  return win;
}

/** endSession drives the roster row's End verb (menu → confirm). */
async function endSession(page: Page, win: ReturnType<Page['locator']>) {
  const row = win.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]').first();
  await expect(row).toBeVisible({ timeout: 20_000 });
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  const menu = page.locator('[data-testid="agents-row-actions"]');
  await expect(menu).toBeVisible();
  await menu.locator('[data-testid="agents-menu-end"]').click();
  await menu.locator('[data-testid="agents-menu-end-confirm"]').click();
}

test('ending a session closes the terminal its agent left running', async ({ page, router }) => {
  test.setTimeout(60_000);
  // A command that will not finish on its own: the only kind that can
  // outlive its session.
  const win = await openAgent(page, router.url, 'runcmd sleep 300');

  // The terminal is live in the transcript, and agentd owns its pty.
  await expect(win.locator('[data-testid="agent-terminal"]')).toBeVisible({ timeout: 20_000 });
  const created = await router.waitForLog(/agentd: terminal\/create key=(acp:\d+) id=(\d+)/, 20_000);
  const [, key, id] = created.match(/key=(acp:\d+) id=(\d+)/)!;

  const cursor = router.logCursor();
  await endSession(page, win);

  // BE half: the session ended AND its terminal was closed with the
  // reason, and the pty child actually exited (its onClose fired).
  await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);
  await router.waitForLog(new RegExp(`agentd: terminal closed key=${key} id=${id} reason="session ended"`), 15_000, cursor);
  await router.waitForLog(new RegExp(`agentd: terminal exited key=${key} ch=${id} reason=session ended`), 15_000, cursor);

  // FE half: no row left to act on.
  await expect(win.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
});

test('ending a session takes its pending question off every rail', async ({ page, router }) => {
  test.setTimeout(60_000);
  const win = await openAgent(page, router.url, 'please ask before running');

  // The question is up: inline in the transcript and in the roster pane
  // (and the desktop rail, which renders the same queue).
  await expect(win.getByText('echo hello > /tmp/wash-e2e-fake')).toBeVisible({ timeout: 20_000 });
  await expect(page.locator('[data-testid="agents-ask"]').first()).toBeVisible({ timeout: 10_000 });
  const before = await page.locator('[data-testid="agents-ask"]').count();
  expect(before).toBeGreaterThan(0);

  const cursor = router.logCursor();
  await endSession(page, win);

  // BE half: the question was cancelled BECAUSE the session ended — not
  // answered, not expired — and the session ended.
  await router.waitForLog(/agentd: ask cancelled id=ask-\d+ row=acp:\d+ tool=Bash reason="session ended"/, 15_000, cursor);
  await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);

  // FE half: nothing left to answer, anywhere. A question surviving here
  // is one "Always allow" could still write a rule for.
  await expect(page.locator('[data-testid="agents-ask"]')).toHaveCount(0, { timeout: 15_000 });
  await expect(win.getByRole('button', { name: /^Allow(\s|$)/ })).toHaveCount(0, { timeout: 15_000 });
  await expect(win.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]')).toHaveCount(0, { timeout: 15_000 });
});
