// An adapter that dies mid-turn is a fact on screen, not a mystery
// (docs/Review-findings.md, 2026-09-08 sweep, "No adapter-exit watcher").
//
// Nothing selected on the ACP client's Done(): a crashed adapter kept its
// roster row and idle-hold, and the next prompt failed with only a red
// dot. agentd now watches each session's adapter; on exit the row goes to
// failed/exited, the transcript says the agent exited — with the last
// thing it wrote to stderr — and the session's questions and terminals
// are released, while History keeps the entry to reopen.

import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function openAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  return startAgentSession(page, prompt);
}

test('an adapter that dies mid-turn fails the row and says so in the transcript', async ({ page, router }) => {
  test.setTimeout(60_000);
  const win = await openAgent(page, router.url, 'say something');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  const cursor = router.logCursor();
  const composer = win.locator('textarea');
  await composer.fill('please crash now');
  await composer.press('Enter');

  // BE half: agentd saw the exit, with the adapter's last words.
  await router.waitForLog(/agentd: acp adapter exited key=acp:\d+ agent=codex session=fake-session-1 .*stderr=".*simulated crash/, 20_000, cursor);
  await router.waitForLog(/agentd: acp row key=acp:\d+ agent=codex state=failed/, 10_000, cursor);

  // FE half: the transcript explains, and the status line is red rather
  // than green or "working" forever.
  await expect(win.getByText(/The agent exited unexpectedly/)).toBeVisible({ timeout: 20_000 });
  await expect(win.getByText(/simulated crash \(token expired\)/)).toBeVisible();
  await expect(win.getByText(/failed/).first()).toBeVisible({ timeout: 10_000 });
  await expect(win.locator('[data-testid="agent-stop"]')).toHaveCount(0);

  // The session is still in History to reopen — the exit did not eat
  // the record. History is the Agents manager's pane now, not a menu on
  // this window; searched, because the pane's list was fetched when the
  // manager mounted, before this session existed.
  const history = page.locator('wash-app-agents [data-testid="agents-history-pane"]');
  await history.locator('[data-testid="ai-history-search"]').fill('say something');
  const histRow = history.locator('[data-testid="ai-history-row"]').filter({ hasText: 'Fake conversation' });
  await expect(histRow).toBeVisible({ timeout: 15_000 });
  // ...and as something to RESUME, not "go to": the failed row is still
  // on the roster, but nothing is behind it.
  await expect(histRow).toHaveAttribute('data-action', 'resume');

  // A prompt into the dead session is told so, rather than vanishing.
  const c2 = router.logCursor();
  await composer.fill('anyone there?');
  await composer.press('Enter');
  await router.waitForLog(/agentd: acp prompt for unknown session key=acp:\d+/, 10_000, c2);
  await expect(page.getByText('That session has ended').first()).toBeVisible({ timeout: 10_000 });
});
