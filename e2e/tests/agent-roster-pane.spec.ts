// The Agent app's roster pane (docs/SIDEBAR.md M2a).
//
// M1 gave the rail per-host counts. M2 moves the roster — and next the
// verbs — into com.wash.ai, because that is where they can be correct: an
// app talking to its own host's agentd carries a router-attested sender,
// so `launchOn(B, com.wash.ai)` gets B's roster with no new addressing.
// This spec is the local half of that claim; the two-router half lands
// with M2c.
//
// Same fake adapter as agent-session.spec.ts: e2e/fixtures/acp-fake on a
// PATH this test controls, so the suite needs no API key and no network.

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

/** openAgentWindow launches a fresh Agent window and returns it. */
async function openAgentWindow(page: Page, nth: number) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').nth(nth);
  await expect(win).toBeVisible();
  return win;
}

/** startSession drives the launcher in an already-open Agent window. */
async function startSession(win: ReturnType<Page['locator']>, prompt: string) {
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
}

test('the roster pane lists agentd\'s sessions and switches the detail pane', async ({ page, router }) => {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // One session: the pane stays shut. A list of one is width spent to say
  // nothing you can't already see in the window you're looking at.
  const first = await openAgentWindow(page, 0);
  await startSession(first, 'first session');
  await expect(first.locator('[data-testid="ai-roster-pane"]')).toHaveCount(0);

  // It is reachable on demand even so.
  await first.locator('[data-testid="ai-roster-toggle"]').click();
  const pane = first.locator('[data-testid="ai-roster-pane"]');
  await expect(pane).toBeVisible();
  await expect(pane.locator('[data-testid^="agents-row-"]')).toHaveCount(1);

  // The row the window is showing reads as the current one.
  await expect(pane.locator('[data-testid^="agents-row-"][data-active="true"]')).toHaveCount(1);

  // A second session in a second window. Both windows' rosters now list
  // BOTH sessions — the roster is agentd's, not the window's, which is the
  // whole reason it can answer for a host rather than for a window.
  const second = await openAgentWindow(page, 1);
  await startSession(second, 'second session');
  await expect(pane.locator('[data-testid^="agents-row-"]')).toHaveCount(2, { timeout: 20_000 });

  // And with more than one session to choose between, the pane opens
  // itself in the new window — that is when a list earns its width.
  await expect(second.locator('[data-testid="ai-roster-pane"]')).toBeVisible({ timeout: 10_000 });

  // Master-detail: activating the OTHER row re-points this window's detail
  // pane at that session, and its transcript replays into it. Before M2a a
  // window could only ever show the session it started.
  //
  // Driven from the SECOND window deliberately: the windows overlap on the
  // desktop, and the newer one is on top — clicking the first window's
  // pane through it hits the wrong window's row (playwright catches this
  // as a pointer-event interception rather than silently misclicking).
  const topPane = second.locator('[data-testid="ai-roster-pane"]');
  await expect(topPane.locator('[data-testid^="agents-row-"]')).toHaveCount(2, { timeout: 20_000 });
  const otherRow = topPane.locator('[data-testid^="agents-row-"][data-active="false"]').first();
  await expect(otherRow).toBeVisible();
  // Remember WHICH row by its stable id: the locator above selects on
  // data-active="false", so once the click flips it the locator no longer
  // matches its own element.
  const otherKey = await otherRow.getAttribute('data-testid');
  await otherRow.click();

  // The first session's transcript is now in the second window.
  await expect(second.getByText('first session')).toBeVisible({ timeout: 20_000 });

  // Exactly one row is current, and it is the one just picked.
  await expect(topPane.locator('[data-testid^="agents-row-"][data-active="true"]')).toHaveCount(1);
  await expect(topPane.locator(`[data-testid="${otherKey}"]`)).toHaveAttribute('data-active', 'true');
});
