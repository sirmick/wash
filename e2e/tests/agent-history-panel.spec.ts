// The History panel, end to end (GH #21).
//
// The component test covers the panel's shape and keyboard; the Go tests
// cover the index and the search. Neither can show the thing that makes
// the feature real: that a conversation you had a minute ago is findable
// BY ITS CONTENT, through agentd, from a window that never saw it.
//
// That round trip is FE → wash-ai → agentd → the stored transcripts on
// disk → back. Every hop is somewhere the feature can quietly not work.

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

async function startAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Agent', exact: true })
    .click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

async function openHistory(page: Page, win: ReturnType<Page['locator']>) {
  await win.locator('[data-testid="ai-menubar-history"]').click();
  await expect(page.locator('[data-testid="ai-menu-history"]')).toBeVisible();
  await page.locator('[data-testid="ai-menu-history-browse"]').click();
  const panel = page.locator('[data-testid="ai-history-panel"]');
  await expect(panel).toBeVisible();
  return panel;
}

test.describe('agent history panel', () => {
  test('lists the session that just ran, with its metadata', async ({ page, router }) => {
    test.setTimeout(60_000);
    const win = await startAgent(page, router.url, 'the quokka protocol');
    const panel = await openHistory(page, win);

    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });
    // The agent comes from the stored summary, not from anything this
    // window happens to be holding.
    await expect(rows.first().locator('[data-testid="ai-history-agent"]')).toHaveText('codex');
    // Still running, so it has no ending — and the list says so, because
    // resuming an unfinished session behaves differently.
    await expect(rows.first().locator('[data-testid="ai-history-unfinished"]')).toBeVisible();
  });

  test('search finds a session by what was SAID in it', async ({ page, router }) => {
    test.setTimeout(60_000);
    const win = await startAgent(page, router.url, 'the quokka protocol');
    const panel = await openHistory(page, win);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    // A word that appears ONLY in the conversation — not in the title,
    // the agent name, the model or the directory. Matching it proves
    // agentd actually read the transcript off disk.
    await panel.locator('[data-testid="ai-history-search"]').fill('quokka');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    // And a word that appears nowhere is empty, not everything — the
    // failure mode where a broken filter looks like a working one.
    await panel.locator('[data-testid="ai-history-search"]').fill('wombat');
    await expect(rows).toHaveCount(0, { timeout: 15_000 });
    await expect(panel.locator('[data-testid="ai-history-empty"]')).toContainText('Nothing matches');
  });

  test('picking a session resumes it', async ({ page, router }) => {
    test.setTimeout(60_000);
    const win = await startAgent(page, router.url, 'resume me later');
    const panel = await openHistory(page, win);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    const cursor = router.logCursor();
    await rows.first().click();

    // The panel closes and agentd is asked to reopen it. Asserted on the
    // router log because that is the hop the FE cannot fake.
    await expect(panel).toHaveCount(0);
    await router.waitForLog(/agentd: (acp session resumed|resume )/, 20_000, cursor);
  });
});
