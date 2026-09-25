// The launcher's Permissions row is the remembered default: the adapter's
// own approval preset (by the names it reported the last time it ran) and
// wash's auto-approval. A change writes agents.json; Start sends what the
// row shows; the session begins in that preset with yolo on, and says so.
//
// The fake adapter (codex-acp) offers read-only / agent / agent-full-access
// and confirms a set_mode with current_mode_update, like the real one.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, chooseAgent, openAgents } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

test('the Permissions row is remembered in agents.json and applied at start', async ({ page, router }) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const manager = await openAgents(page);

  // Codex has never run here: no presets to offer yet, and it says so.
  await chooseAgent(manager, 'codex');
  const mode = manager.locator('[data-testid="ai-mode-select"]');
  await expect(mode).toBeDisabled();
  await expect(mode.locator('option').first()).toContainText('after it has run once');

  // First session: learns the presets.
  let cursor = router.logCursor();
  await manager.locator('[data-testid="ai-start"]').click();
  await router.waitForLog(/agentd: session settings key=\S+ catalog=\S* model=\S* connection= adapter=codex mode=agent yolo=false/, 25_000, cursor);
  // agentd opened the session's window over the manager; raise the manager.
  await openAgents(page);
  await expect(mode).toBeEnabled({ timeout: 15_000 });
  await expect(mode.locator('option')).toHaveText(["Codex's default", 'Read-only', 'Agent', 'Agent (full access)']);

  // Set the default: read-only, auto-approve. Written, not held in the form.
  cursor = router.logCursor();
  await mode.selectOption('read-only');
  await router.waitForLog(/agentd: launch default mode=map\[codex:read-only\] yolo=false/, 15_000, cursor);
  cursor = router.logCursor();
  await manager.locator('[data-testid="ai-yolo"]').check();
  await router.waitForLog(/agentd: launch default mode=map\[codex:read-only\] yolo=true/, 15_000, cursor);
  const file = JSON.parse(readFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), 'utf8'));
  expect(file.launch).toEqual({ mode: { codex: 'read-only' }, yolo: true });

  // Second session starts that way, and the row and the transcript say so.
  const before = await page.locator('wash-app-ai').count();
  cursor = router.logCursor();
  await manager.locator('[data-testid="ai-start"]').click();
  await router.waitForLog(/agentd: session settings key=\S+ catalog=\S* model=\S* connection= adapter=codex mode=read-only yolo=true/, 25_000, cursor);
  await expect(page.locator('wash-app-ai')).toHaveCount(before + 1, { timeout: 20_000 });
  const win = page.locator('wash-app-ai').nth(before);
  await expect(win.getByText('Auto-approval (yolo) is ON')).toBeVisible({ timeout: 20_000 });

  // The Agents window reopened still shows the remembered default.
  await page.reload();
  await expect(page.locator('wash-app-session')).toBeVisible();
  const again = await openAgents(page);
  await chooseAgent(again, 'codex');
  await expect(again.locator('[data-testid="ai-mode-select"]')).toHaveValue('read-only', { timeout: 15_000 });
  await expect(again.locator('[data-testid="ai-yolo"]')).toBeChecked();
});
