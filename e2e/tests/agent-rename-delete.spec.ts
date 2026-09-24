// Renaming and deleting sessions, end to end (docs/Review-findings.md P2
// → agent; apps/agentd/be/session_admin.go).
//
// A session's title used to be the agent's alone — first wins, nothing
// could change it — and the only way to lose a conversation was `rm` in
// the state dir. Both halves are asserted, because either alone passes
// while the feature is broken: a row can show a new name the backend
// never stored, and a panel can drop a row whose file is still on disk.
//
// Both lists live in the Agents manager now: the running roster (where
// Rename… and End are row verbs) and the History pane. The session itself
// runs in its own controller window, which this spec never needs to touch.

import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, freshHistory, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// The fake adapter always answers session/new with this id, so the
// transcript's path is known up front.
const transcriptPath = (stateHome: string) => join(stateHome, 'wash', 'agent-transcripts', 'fake-session-1.jsonl');

async function startSession(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const win = await startAgentSession(page, 'say something');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

/** rosterRow is the session's row in the manager's Running pane. */
function rosterRow(page: Page): Locator {
  return page.locator('wash-app-agents [data-testid="agents-running-pane"] [data-testid^="agents-row-"]').first();
}

async function endSession(page: Page, router: { waitForLog: (re: RegExp, t?: number, from?: number) => Promise<string>; logCursor: () => number }) {
  const row = rosterRow(page);
  await expect(row).toBeVisible({ timeout: 15_000 });
  const cursor = router.logCursor();
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  await page.locator('[data-testid="agents-menu-end"]').click();
  await page.locator('[data-testid="agents-menu-end-confirm"]').click();
  await router.waitForLog(/agentd: acp session ended key=acp:\d+/, 20_000, cursor);
  await expect(row).toHaveCount(0, { timeout: 15_000 });
}

test.describe('agent session rename and delete', () => {
  test.setTimeout(90_000);

  test('Rename… from the manager roster row names the session everywhere, and on disk', async ({ page, router }) => {
    await startSession(page, router.url);
    const row = rosterRow(page);
    await expect(row).toBeVisible({ timeout: 15_000 });
    // The agent named it first; that is the name being overridden.
    await expect(row.locator('[data-testid="agents-title"]')).toHaveText('Fake conversation', { timeout: 15_000 });

    const cursor = router.logCursor();
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    await page.locator('[data-testid="agents-menu-rename"]').click();
    const input = page.locator('[data-testid="ai-rename-input"]');
    await expect(input).toBeVisible();
    // Prefilled with the current name, selected, so typing replaces it.
    await expect(input).toHaveValue('Fake conversation');
    await input.fill('Quokka triage');
    await input.press('Enter');
    await expect(page.locator('[data-testid="ai-rename-dialog"]')).toHaveCount(0);

    // FE half: the roster row, and the History panel that reads files.
    await expect(row.locator('[data-testid="agents-title"]')).toHaveText('Quokka triage', { timeout: 15_000 });
    // BE half: agentd stored it, in the log and in the transcript itself.
    await router.waitForLog(/agentd: session renamed key=acp:\d+ session=fake-session-1 title="Quokka triage"/, 20_000, cursor);
    await expect.poll(() => readFileSync(transcriptPath(router.xdgStateHome), 'utf8'), { timeout: 15_000 })
      .toContain('"user_title":"Quokka triage"');

    const panel = await freshHistory(page);
    await expect(panel.locator('[data-testid="ai-history-title"]').first()).toHaveText('Quokka triage', { timeout: 15_000 });
  });

  test('Delete removes the transcript from disk and the row from History', async ({ page, router }) => {
    await startSession(page, router.url);
    const file = transcriptPath(router.xdgStateHome);
    await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(true);

    // A running session cannot be deleted: the item is there, and says why.
    let panel = await freshHistory(page);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });
    await rows.first().locator('[data-testid="ai-history-verbs"]').click();
    const del = page.locator('[data-testid="ai-history-menu-delete"]');
    await expect(del).toBeDisabled();
    await expect(del).toContainText('still running');
    // Dismissed by clicking away — @wash/ui's Menu closes on an outside
    // pointerdown and has no Escape handling, so a keypress here left the
    // menu open over the manager for the End verbs below to fight with.
    await panel.locator('[data-testid="ai-history-search"]').click();
    await expect(page.locator('[data-testid="ai-history-actions"]')).toHaveCount(0);

    await endSession(page, router);

    panel = await freshHistory(page);
    const rowsAfterEnd = panel.locator('[data-testid="ai-history-row"]');
    await expect(rowsAfterEnd).toHaveCount(1, { timeout: 15_000 });
    await expect(rowsAfterEnd.first()).toHaveAttribute('data-action', 'resume');
    const cursor = router.logCursor();
    await rowsAfterEnd.first().locator('[data-testid="ai-history-verbs"]').click();
    await page.locator('[data-testid="ai-history-menu-delete"]').click();
    await expect(page.locator('[data-testid="ai-delete-confirm"]')).toBeVisible();
    await page.locator('[data-testid="ai-delete-confirm-yes"]').click();

    // Both halves: the list is empty because the file is gone.
    await router.waitForLog(/agentd: session deleted session=fake-session-1/, 20_000, cursor);
    await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(false);
    await expect(rowsAfterEnd).toHaveCount(0, { timeout: 15_000 });
    await expect(panel.locator('[data-testid="ai-history-empty"]')).toBeVisible();
  });

  test('Delete older than… prunes finished sessions by age', async ({ page, router }) => {
    await startSession(page, router.url);
    const file = transcriptPath(router.xdgStateHome);
    await endSession(page, router);
    await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(true);

    const panel = await freshHistory(page);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    // A month's horizon keeps a session that ended a second ago.
    let cursor = router.logCursor();
    await panel.locator('[data-testid="ai-history-prune"]').click();
    await page.locator('[data-testid="ai-prune-age"]').selectOption(String(30 * 24 * 3600e3));
    await page.locator('[data-testid="ai-prune-confirm"]').click();
    await router.waitForLog(/agentd: sessions pruned max_age=720h0m0s deleted=0/, 20_000, cursor);
    expect(existsSync(file)).toBe(true);
    await expect(rows).toHaveCount(1);

    // "Any age" takes every finished session.
    cursor = router.logCursor();
    await panel.locator('[data-testid="ai-history-prune"]').click();
    await page.locator('[data-testid="ai-prune-age"]').selectOption('0');
    await page.locator('[data-testid="ai-prune-confirm"]').click();
    await router.waitForLog(/agentd: sessions pruned max_age=0s deleted=1/, 20_000, cursor);
    await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(false);
    await expect(rows).toHaveCount(0, { timeout: 15_000 });
  });
});
