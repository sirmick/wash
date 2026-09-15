// The History panel, end to end (GH #21).
//
// The component test covers the panel's shape and keyboard; the Go tests
// cover the index and the search. Neither can show the thing that makes
// the feature real: that a conversation you had a minute ago is findable
// BY ITS CONTENT, through agentd, from a window that never saw it.
//
// That round trip is FE → wash-ai → agentd → the stored transcripts on
// disk → back. Every hop is somewhere the feature can quietly not work.
//
// History is no longer a modal a session window opens from its menubar:
// it is a permanent pane of the Agents manager (com.wash.agents), and the
// window that never saw the conversation is the manager itself — the
// session ran in its own controller window. The pane answers from the
// store as of the manager's mount, so each test looks through a reopened
// manager (freshHistory) — the equivalent of opening the old modal.

import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, freshHistory, openAgents, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const win = await startAgentSession(page, prompt);
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
  return win;
}

test.describe('agent history panel', () => {
  test('is available before starting a new session', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);

    // Beside the launcher, not behind a menu: nothing has to be started
    // (or even opened) to look at what ran before.
    await expect(manager.getByRole('button', { name: 'Start session' })).toBeVisible();
    const panel = manager.locator('[data-testid="agents-history-pane"] [data-testid="ai-history-panel"]');
    await expect(panel).toBeVisible();
    const search = panel.locator('[data-testid="ai-history-search"]');
    await expect(search).toBeVisible();
    await expect(panel.locator('[data-testid="ai-history-empty"]')).toBeVisible({ timeout: 15_000 });

    // Browsing did not create an adapter session behind the panel: a
    // search is a query, and no controller window appeared for it.
    await search.fill('anything');
    await expect(panel.locator('[data-testid="ai-history-empty"]')).toContainText('Nothing matches', { timeout: 15_000 });
    await expect(manager.getByRole('button', { name: 'Start session' })).toBeVisible();
    await expect(page.locator('wash-app-ai')).toHaveCount(0);
    expect(router.log()).not.toMatch(/agentd: opening controller/);
  });

  test('lists the session that just ran, with its metadata', async ({ page, router }) => {
    test.setTimeout(60_000);
    await startAgent(page, router.url, 'the quokka protocol');
    const panel = await freshHistory(page);

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
    await startAgent(page, router.url, 'the quokka protocol');
    const panel = await freshHistory(page);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    // A word that appears ONLY in the conversation — not in the title,
    // the agent name, the model or the directory. Matching it proves
    // agentd actually read the transcript off disk.
    await panel.locator('[data-testid="ai-history-search"]').fill('quokka');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });

    // And the row says WHY it matched, with the term marked — otherwise a
    // result is a title you still have to open to identify.
    const snippet = rows.first().locator('[data-testid="ai-history-snippet"]');
    await expect(snippet).toContainText('quokka', { timeout: 15_000 });
    await expect(rows.first().locator('[data-testid="ai-history-hit"]').first())
      .toHaveText('quokka');

    // And a word that appears nowhere is empty, not everything — the
    // failure mode where a broken filter looks like a working one.
    await panel.locator('[data-testid="ai-history-search"]').fill('wombat');
    await expect(rows).toHaveCount(0, { timeout: 15_000 });
    await expect(panel.locator('[data-testid="ai-history-empty"]')).toContainText('Nothing matches');
  });

  test('picking a session that is still running goes to it', async ({ page, router }) => {
    // It used to do nothing at all: the row was live, so it was neither
    // resumable (that would fork a second adapter onto one conversation)
    // nor reattachable (it has a window already), and an inert row in a
    // list you opened to get somewhere is its own defect
    // (docs/AGENT_UX.md N1).
    test.setTimeout(60_000);
    await startAgent(page, router.url, 'resume me later');
    const panel = await freshHistory(page);
    const rows = panel.locator('[data-testid="ai-history-row"]');
    await expect(rows).toHaveCount(1, { timeout: 15_000 });
    // The row says which of the three it is, and it is not "resume".
    await expect(rows.first()).toHaveAttribute('data-action', 'focus');

    const cursor = router.logCursor();
    await rows.first().click();

    // agentd raises the one controller that session already has. Asserted
    // on the router log because that is the hop the FE cannot fake — and
    // specifically NOT on a resume, which would be the bug, nor on a
    // second controller being opened for the same session.
    await router.waitForLog(/agentd: focus key=acp:\d+ raising controller=\S+/, 20_000, cursor);
    const since = router.log().slice(cursor);
    expect(since).not.toMatch(/acp session resumed/);
    expect(since).not.toMatch(/agentd: opening controller/);
    await expect(page.locator('wash-app-ai')).toHaveCount(1);
  });
});

test('search is words, all of them, in any order', async ({ page, router }) => {
  // The old search was one literal substring, so "quokka protocol" only
  // matched if those words were adjacent in that order. A conversation is
  // the unit now: every word has to appear somewhere in it.
  test.setTimeout(60_000);
  await startAgent(page, router.url, 'the quokka protocol, and separately a wombat');
  const panel = await freshHistory(page);
  const rows = panel.locator('[data-testid="ai-history-row"]');
  await expect(rows).toHaveCount(1, { timeout: 15_000 });

  const search = panel.locator('[data-testid="ai-history-search"]');
  await search.fill('quokka wombat');
  await expect(rows).toHaveCount(1, { timeout: 15_000 });
  await search.fill('wombat quokka');
  await expect(rows).toHaveCount(1, { timeout: 15_000 });

  // Every term is required: one miss rules the session out even though
  // the other matches.
  await search.fill('quokka aardvark');
  await expect(rows).toHaveCount(0, { timeout: 15_000 });
  await expect(panel.locator('[data-testid="ai-history-empty"]')).toContainText('Nothing matches');
});
