// Stacks on the new-agent screen (apps/agentd/be/stacks.go): pick a stack and
// a tier, and agentd starts the tier's adapter through its connection and
// applies its model and effort.
//
// The shipped "OpenRouter budget" stack runs OpenCode through OpenRouter. The
// fake stands in for OpenCode here (a symlink named `opencode`), answering
// as the real one was seen to: openrouter/* models are offered only when
// OPENROUTER_API_KEY reaches it. So a session that starts on the coding
// tier's model proves the whole chain — the key store, the connection's
// environment, and the tier's settings — without a network.

import { chmodSync, mkdirSync, mkdtempSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, openAgents } from '../fixtures/agents';

const opencodeDir = mkdtempSync(join(tmpdir(), 'wash-e2e-opencode-'));
symlinkSync(join(FAKE_DIR, 'codex-acp'), join(opencodeDir, 'opencode'));

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${opencodeDir}:${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

function writeKeys(configHome: string, keys: Record<string, string>) {
  const dir = join(configHome, 'wash');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'keys.json'), JSON.stringify(keys));
  chmodSync(join(dir, 'keys.json'), 0o600);
}

test.describe('stacks', () => {
  test.setTimeout(90_000);

  test('without a key the OpenRouter stack is greyed with the reason', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    const option = manager.locator('[data-testid="ai-stack-select"] option[value="openrouter"]');
    // toBeDisabled does not read an <option>'s own disabled state.
    await expect(option).toHaveJSProperty('disabled', true);
    await expect(option).toContainText('no openrouter key set');
  });

  test('OpenRouter budget / coding runs OpenCode on the tier model through the key', async ({ page, router }) => {
    writeKeys(router.xdgConfigHome, { openrouter: 'sk-or-e2e-0000-wxyz' });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);

    const stack = manager.locator('[data-testid="ai-stack-select"]');
    // The key was on disk before agentd started, so the stack is available
    // from the first roster.
    await expect(stack.locator('option[value="openrouter"]')).toHaveJSProperty('disabled', false, { timeout: 15_000 });
    await stack.selectOption('openrouter');
    await manager.locator('[data-testid="ai-tier-select"]').selectOption('coding');
    await expect(manager.locator('[data-testid="ai-tier-summary"]'))
      .toHaveText('OpenCode · openrouter/deepseek/deepseek-v4-pro-0813 · effort high · via openrouter');

    const before = await page.locator('wash-app-ai').count();
    const cursor = router.logCursor();
    await manager.locator('[data-testid="ai-start"]').click();

    // agentd applied the tier: the model only exists with the key, and the
    // effort only once that model is chosen.
    await router.waitForLog(
      /agentd: session settings key=\S+ stack=openrouter tier=coding connection=opencode@openrouter adapter=opencode effective=map\[effort:high mode:build model:openrouter\/deepseek\/deepseek-v4-pro-0813\]/,
      25_000,
      cursor,
    );

    await expect(page.locator('wash-app-ai')).toHaveCount(before + 1, { timeout: 20_000 });
    const win = page.locator('wash-app-ai').nth(before);
    const composer = win.locator('textarea');
    await expect(composer).toBeVisible({ timeout: 20_000 });
    await composer.fill('launchinfo');
    await composer.press('Enter');
    // The adapter's own report: the key arrived, and so did the permission
    // config that makes OpenCode ask before edits and commands.
    await expect(win.getByText(/KEYS<<openrouter=wxyz auth-token= opencode-config=true>>/)).toBeVisible({ timeout: 20_000 });
  });

  test('a model the adapter does not offer fails the start and names the ones it does', async ({ page, router }) => {
    writeKeys(router.xdgConfigHome, { openrouter: 'sk-or-e2e-0000-wxyz' });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    const stack = manager.locator('[data-testid="ai-stack-select"]');
    await expect(stack.locator('option[value="openrouter"]')).toHaveJSProperty('disabled', false, { timeout: 15_000 });
    await stack.selectOption('openrouter');
    await manager.locator('[data-testid="ai-advanced"] summary').click();
    await manager.locator('[data-testid="ai-model-input"]').fill('openrouter/nobody/imaginary-1');
    await manager.locator('[data-testid="ai-start"]').click();
    await expect(manager.locator('[data-testid="ai-start-error"]'))
      .toContainText('available values: opencode/big-pickle, openrouter/', { timeout: 25_000 });
  });
});
