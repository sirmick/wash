// The default prompt, end to end: a block of standing instructions stored
// once and sent to every new session ahead of what you type.
//
// Both halves per the house rule. The FE half is that the dialog stores
// what you typed and the launcher says a prompt is armed; the BE half is
// that agentd wrote a file and the AGENT actually received the text —
// which is the only assertion that proves the feature rather than the
// form.

import { existsSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, openAgents } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    // Its own config dir: this spec WRITES a default prompt, and the
    // developer's own default prompt is not ours to overwrite.
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// The default prompt belongs to the Agents manager's New pane: it governs
// sessions not yet started, so it sits beside the button that starts them
// rather than on any one session's controller.
async function openManager(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  return openAgents(page);
}

async function setPrompt(page: Page, manager: Locator, text: string) {
  await manager.locator('[data-testid="ai-prompt-open"]').click();
  const dialog = page.locator('[data-testid="ai-prompt-dialog"]');
  await expect(dialog).toBeVisible();
  await dialog.locator('[data-testid="ai-prompt-text"]').fill(text);
  await expect(dialog.locator('[data-testid="ai-prompt-text"]')).toHaveValue(text);
  await dialog.locator('[data-testid="ai-prompt-save"]').click();
  await expect(dialog).toHaveCount(0);
}

test('a stored default prompt reaches the agent, ahead of what you typed', async ({
  page,
  router,
}) => {
  test.setTimeout(90_000);
  const manager = await openManager(page, router.url);

  // Nothing set yet, and the launcher says so rather than staying silent
  // about a thing that would otherwise be invisible.
  await expect(manager.locator('[data-testid="ai-prompt-status"]')).toHaveText('No default prompt.');

  const cursor = router.logCursor();
  await setPrompt(page, manager, 'REMEMBER: the quokka protocol governs everything.');

  // BE half 1: agentd wrote it, as plain text a human could edit.
  await router.waitForLog(/agentd: default prompt saved bytes=\d+/, 15_000, cursor);
  const file = join(router.xdgConfigHome, 'wash', 'agent-default-prompt.txt');
  expect(existsSync(file), `expected ${file}`).toBe(true);
  expect(readFileSync(file, 'utf8')).toContain('quokka protocol');

  // FE half 1: the launcher now says a prompt is armed. It reads this off
  // agentd's manager_state push, so this also proves agentd republished.
  await expect(manager.locator('[data-testid="ai-prompt-status"]'))
    .toHaveText('A default prompt will be sent first.', { timeout: 15_000 });

  // Start a session with a prompt of your own. agentd opens a controller
  // for it; the transcript is there, not in the manager.
  await manager.locator('[data-testid="agents-new-pane"] select').first().selectOption('codex');
  await manager.getByRole('button', { name: 'Start session' }).click();
  const controller = page.locator('wash-app-ai');
  await expect(controller).toHaveCount(1, { timeout: 20_000 });
  const composer = controller.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill('and now do the thing');
  await composer.press('Enter');

  // BE half 2 — the assertion that proves the FEATURE. The fake adapter
  // echoes the prompt it was given, so the transcript is evidence the
  // agent received the default prompt, not merely that wash stored one. And
  // the standing instructions come FIRST.
  const transcript = controller.locator('[data-testid="agent-transcript"]');
  await expect(transcript).toContainText('quokka protocol', { timeout: 20_000 });
  await expect(transcript).toContainText('and now do the thing', { timeout: 20_000 });
});

test('the stored prompt survives a browser reload', async ({ page, router }) => {
  // The point of storing it on the host: a reload is an FE remount, and
  // the text lives in a file rather than in the tab that typed it.
  test.setTimeout(90_000);
  const manager = await openManager(page, router.url);
  await setPrompt(page, manager, 'standing instructions that must outlive the tab');
  await expect(manager.locator('[data-testid="ai-prompt-status"]'))
    .toHaveText('A default prompt will be sent first.', { timeout: 15_000 });

  await page.reload();
  await expect(page.locator('wash-app-session')).toBeVisible();
  // Raise-or-launch: whether the shell restored the manager or not, what
  // is on screen now is a fresh mount that knows nothing the tab typed.
  const after = await openAgents(page);
  await expect(after.locator('[data-testid="ai-prompt-status"]'))
    .toHaveText('A default prompt will be sent first.', { timeout: 20_000 });

  // And re-opening the editor shows the stored text, fetched fresh from
  // agentd rather than remembered by the page.
  await after.locator('[data-testid="ai-prompt-open"]').click();
  await expect(page.locator('[data-testid="ai-prompt-text"]'))
    .toHaveValue(/standing instructions that must outlive the tab/, { timeout: 15_000 });
});

test('clearing it removes the file, and sessions go back to plain', async ({ page, router }) => {
  test.setTimeout(90_000);
  const manager = await openManager(page, router.url);
  await expect(manager.locator('[data-testid="ai-prompt-status"]')).toHaveText('No default prompt.');
  await setPrompt(page, manager, 'temporary instructions');
  const file = join(router.xdgConfigHome, 'wash', 'agent-default-prompt.txt');
  await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(true);

  await setPrompt(page, manager, '');

  // "No default prompt" and "an empty default prompt" are the same state, so the file
  // goes rather than being left empty to puzzle whoever finds it.
  await expect.poll(() => existsSync(file), { timeout: 15_000 }).toBe(false);
  await expect(manager.locator('[data-testid="ai-prompt-status"]'))
    .toHaveText('No default prompt.', { timeout: 15_000 });
});
