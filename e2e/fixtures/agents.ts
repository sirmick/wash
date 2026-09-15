// Shared drivers for the two agent surfaces (docs/AGENT_APP.md §9).
//
// com.wash.agents is the singleton manager — New, History, Running — and
// com.wash.ai is a hidden, one-per-session controller that only agentd
// opens. Every agent spec used to start from "Start menu → Agent → fill the
// launcher in that window"; that window no longer has a launcher, so the
// route lives here once instead of being re-derived per spec.
//
// The agent itself is e2e/fixtures/acp-fake built as `codex-acp` into
// out/e2e, which specs put on the router's PATH.

import { fileURLToPath } from 'node:url';
import type { Locator, Page } from '@playwright/test';
import { expect } from './router';

export const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

/** The router apps an agent spec needs: the manager AND the controller. */
export const AGENT_APPS = ['session', 'agentd', 'agents', 'ai', 'notify'] as const;

/** openAgents raises (or launches) the singleton Agents manager. */
export async function openAgents(page: Page): Promise<Locator> {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agents', exact: true }).click();
  const manager = page.locator('wash-app-agents');
  await expect(manager).toBeVisible();
  return manager;
}

/**
 * startAgentSession starts a codex session from the manager and returns
 * the controller window agentd opened for it. With a prompt, it is sent and
 * the composer is left ready for the next one.
 */
export async function startAgentSession(
  page: Page,
  prompt?: string,
  opts: { cwd?: string } = {},
): Promise<Locator> {
  const before = await page.locator('wash-app-ai').count();
  const manager = await openAgents(page);
  if (opts.cwd !== undefined) await pickLauncherFolder(page, manager, opts.cwd);
  await manager.locator('[data-testid="agents-new-pane"] select').first().selectOption('codex');
  await manager.getByRole('button', { name: 'Start session' }).click();
  await expect(page.locator('wash-app-ai')).toHaveCount(before + 1, { timeout: 20_000 });
  const win = page.locator('wash-app-ai').nth(before);
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  if (prompt !== undefined) {
    await composer.fill(prompt);
    await composer.press('Enter');
  }
  return win;
}

/**
 * pickLauncherFolder sets the New pane's folder through its picker. The cwd
 * is the sandbox boundary, so a spec about that boundary has to set it
 * explicitly rather than inherit $HOME.
 */
export async function pickLauncherFolder(page: Page, manager: Locator, dir: string): Promise<void> {
  await manager.locator('[data-testid="agents-new-pane"]').getByRole('button', { name: 'Choose…' }).click();
  const picker = page.locator('[data-testid="ai-folder-picker"]');
  await expect(picker).toBeVisible();
  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(dir);
  await bar.press('Enter');
  await picker.locator('[data-testid="fp-confirm"]').click();
  await expect(picker).toBeHidden();
}

/**
 * windowOf is the shell frame around an app element — where the title bar
 * and its close button live. The manager is usually open beside a
 * controller, so "the first close button on the page" is no longer the
 * session window's.
 *
 * Walked UP from the app element rather than filtered with `{ has: app }`:
 * `has` re-evaluates the inner locator inside each candidate, so with two
 * controllers open `has: wash-app-ai >> nth=0` matched BOTH frames (each
 * contains a first wash-app-ai of its own) and every click on the result
 * was a strict-mode violation.
 */
export function windowOf(_page: Page, app: Locator): Locator {
  return app.locator('xpath=ancestor::div[contains(concat(" ", normalize-space(@class), " "), " wash-window ")][1]');
}

/** closeButtonOf is that frame's own close button. */
export function closeButtonOf(page: Page, app: Locator): Locator {
  return windowOf(page, app).locator('[data-testid="window-close"]');
}

/**
 * freshHistory closes and reopens the manager and returns its History
 * pane. The pane asks agentd once, when the manager mounts — and a spec
 * opened the manager to START the session it now wants to find, so that
 * answer predates the conversation (and a later rename or end). Reopening
 * is the user's way to look again, as opening the old History modal was.
 */
export async function freshHistory(page: Page): Promise<Locator> {
  const manager = page.locator('wash-app-agents');
  if (await manager.count()) {
    await closeButtonOf(page, manager).click();
    await expect(manager).toHaveCount(0, { timeout: 10_000 });
  }
  const reopened = await openAgents(page);
  const panel = reopened.locator('[data-testid="agents-history-pane"] [data-testid="ai-history-panel"]');
  await expect(panel).toBeVisible();
  return panel;
}
