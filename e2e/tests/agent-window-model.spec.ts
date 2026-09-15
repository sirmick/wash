import { fileURLToPath } from 'node:url';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'agents', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

test('manager is singleton and a session has one dedicated controller', async ({ page, router }) => {
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agents', exact: true }).click();
  const manager = page.locator('wash-app-agents');
  await expect(manager).toBeVisible();
  await expect(manager.locator('[data-testid="agents-new-pane"]')).toBeVisible();
  await expect(manager.locator('[data-testid="agents-history-pane"]')).toBeVisible();
  await expect(manager.locator('[data-testid="agents-running-pane"]')).toBeVisible();
  await expect(manager.locator('[data-testid="agents-manager-splitter"]')).toBeVisible();
  const paneOrder = await manager.locator('[data-testid="agents-new-pane"], [data-testid="agents-manager-splitter"], [data-testid="agents-running-pane"]').evaluateAll(
    (nodes) => nodes.map((node) => node.getAttribute('data-testid')),
  );
  expect(paneOrder).toEqual(['agents-new-pane', 'agents-manager-splitter', 'agents-running-pane']);
  await expect(manager.locator('[data-testid="ai-history-panel"]')).toBeVisible();
  await expect(manager.locator('[data-testid="ai-history-close"]')).toHaveCount(0);
  await expect(manager.locator('[data-testid="ai-roster-pane"]')).toBeVisible();

  await manager.locator('select').selectOption('codex');
  await manager.getByRole('button', { name: 'Start session' }).click();

  const controller = page.locator('wash-app-ai');
  await expect(controller.locator('textarea')).toBeVisible({ timeout: 20_000 });
  await expect(controller.locator('[data-testid="ai-roster-pane"]')).toHaveCount(0);
  await expect(manager.locator('[data-testid^="agents-row-"]')).toHaveCount(1);

  // Reopening the manager focuses its singleton; activating the live row
  // focuses the existing controller rather than launching another one.
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agents', exact: true }).click();
  await expect(page.locator('wash-app-agents')).toHaveCount(1);
  await manager.locator('[data-testid^="agents-row-"]').click();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agents', exact: true }).click();
  await manager.locator('[data-testid^="agents-row-"]').click();
  await expect(page.locator('wash-app-ai')).toHaveCount(1);
});
