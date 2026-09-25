// Catalogs on the new-agent screen (apps/agentd/be/catalogs.go): pick a
// catalog and a model, and agentd starts the slot's adapter through its
// connection and applies its model and effort.
//
// The shipped "OpenRouter budget" catalog runs OpenCode through OpenRouter. The
// fake stands in for OpenCode here (a symlink named `opencode`), answering
// as the real one was seen to: openrouter/* models are offered only when
// OPENROUTER_API_KEY reaches it. So a session that starts on the coding
// slot's model proves the whole chain — the key store, the connection's
// environment, and the slot's settings — without a network.

import { chmodSync, mkdirSync, mkdtempSync, readFileSync, statSync, symlinkSync, writeFileSync } from 'node:fs';
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

test.describe('catalogs', () => {
  test.setTimeout(90_000);

  test('the OpenRouter catalogs are greyed until the key is set, and the key is never shown again', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    const option = manager.locator('[data-testid="ai-catalog-select"] option[value="openrouter-budget"]');
    // toBeDisabled does not read an <option>'s own disabled state.
    await expect(option).toHaveJSProperty('disabled', true);
    await expect(option).toContainText('no openrouter key set');
    // Keys live on the Connections tab.
    await manager.locator('[data-testid="agents-tab-connections"]').click();
    await expect(manager.locator('[data-testid="ai-key-status-openrouter"]')).toHaveText('not set');

    const secret = 'sk-or-v1-e2e-secret-0000-wxyz';
    const cursor = router.logCursor();
    await manager.locator('[data-testid="ai-key-input-openrouter"]').fill(secret);
    await manager.locator('[data-testid="ai-key-save-openrouter"]').click();
    await router.waitForLog(/agentd: key openrouter set=true/, 15_000, cursor);

    // Saved once, then shown only as set and its last four characters; the
    // stack that needed it is available straight away.
    await expect(manager.locator('[data-testid="ai-key-status-openrouter"]')).toHaveText('set · …wxyz');
    await expect(manager.locator('[data-testid="ai-key-input-openrouter"]')).toHaveValue('');
    await manager.locator('[data-testid="agents-tab-new"]').click();
    await expect(option).toHaveJSProperty('disabled', false);
    await manager.locator('[data-testid="agents-tab-connections"]').click();
    expect(await manager.innerHTML()).not.toContain(secret);
    expect(router.log()).not.toContain(secret);

    // Beside agents.json, not in it, and readable only by the owner.
    const file = join(router.xdgConfigHome, 'wash', 'keys.json');
    expect(statSync(file).mode & 0o777).toBe(0o600);
    expect(JSON.parse(readFileSync(file, 'utf8'))).toEqual({ openrouter: secret });

    await manager.locator('[data-testid="ai-key-clear-openrouter"]').click();
    await expect(manager.locator('[data-testid="ai-key-status-openrouter"]')).toHaveText('not set');
    await manager.locator('[data-testid="agents-tab-new"]').click();
    await expect(option).toHaveJSProperty('disabled', true);
  });

  // The Catalog tab writes agents.json; the launcher offers the result on
  // the next roster. A reset removes the override and the built-in is
  // back.
  test('a catalog edited on the Catalog tab is what the launcher then starts from', async ({ page, router }) => {
    writeKeys(router.xdgConfigHome, { openrouter: 'sk-or-e2e-0000-wxyz' });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    await manager.locator('[data-testid="agents-tab-catalog"]').click();
    const card = manager.locator('[data-testid="ai-catalog-openrouter-budget"]');
    await expect(card.locator('[data-testid="ai-catalog-origin-openrouter-budget"]')).toHaveText('built in');
    await expect(card.locator('[data-testid="ai-catalog-slot-openrouter-budget-review"]')).toHaveCount(0);
    await card.locator('[data-testid="ai-catalog-model-openrouter-budget-coding"]').fill('openrouter/z-ai/glm-5.3');
    await card.locator('[data-testid="ai-catalog-name-openrouter-budget"]').fill('OpenRouter, GLM for coding');
    const cursor = router.logCursor();
    await card.locator('[data-testid="ai-catalog-save-openrouter-budget"]').click();
    await router.waitForLog(/agentd: catalog openrouter-budget saved/, 15_000, cursor);
    await expect(card.locator('[data-testid="ai-catalog-result-openrouter-budget"]')).toHaveText('Saved.');
    await expect(card.locator('[data-testid="ai-catalog-origin-openrouter-budget"]')).toHaveText('built in, changed here');

    const file = JSON.parse(readFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), 'utf8'));
    expect(file.catalogs['openrouter-budget'].name).toBe('OpenRouter, GLM for coding');
    expect(file.catalogs['openrouter-budget'].slots.coding.model).toBe('openrouter/z-ai/glm-5.3');
    expect(Object.keys(file.catalogs['openrouter-budget'].slots).sort()).toEqual(['coding', 'frontier', 'small']);

    await manager.locator('[data-testid="agents-tab-new"]').click();
    const stack = manager.locator('[data-testid="ai-catalog-select"]');
    await expect(stack.locator('option[value="openrouter-budget"]')).toHaveText('OpenRouter, GLM for coding');
    await stack.selectOption('openrouter-budget');
    await manager.locator('[data-testid="ai-model-select"]').selectOption('coding');
    await expect(manager.locator('[data-testid="ai-model-summary"]')).toHaveText('OpenCode · openrouter/z-ai/glm-5.3 · effort high · via openrouter');

    await manager.locator('[data-testid="agents-tab-catalog"]').click();
    await card.locator('[data-testid="ai-catalog-reset-openrouter-budget"]').click();
    await expect(card.locator('[data-testid="ai-catalog-origin-openrouter-budget"]')).toHaveText('built in');
    await expect(card.locator('[data-testid="ai-catalog-model-openrouter-budget-coding"]')).toHaveValue('openrouter/deepseek/deepseek-v4-pro-0813');
    expect(JSON.parse(readFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), 'utf8')).catalogs ?? {}).toEqual({});
  });

  test("OpenRouter budget / coding runs OpenCode on the slot's model through the key", async ({ page, router }) => {
    writeKeys(router.xdgConfigHome, { openrouter: 'sk-or-e2e-0000-wxyz' });
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);

    const stack = manager.locator('[data-testid="ai-catalog-select"]');
    // The key was on disk before agentd started, so the stack is available
    // from the first roster.
    await expect(stack.locator('option[value="openrouter-budget"]')).toHaveJSProperty('disabled', false, { timeout: 15_000 });
    await stack.selectOption('openrouter-budget');
    await manager.locator('[data-testid="ai-model-select"]').selectOption('coding');
    await expect(manager.locator('[data-testid="ai-model-summary"]'))
      .toHaveText('OpenCode · openrouter/deepseek/deepseek-v4-pro-0813 · effort high · via openrouter');

    const before = await page.locator('wash-app-ai').count();
    const cursor = router.logCursor();
    await manager.locator('[data-testid="ai-start"]').click();

    // agentd applied the slot: the model only exists with the key, and the
    // effort only once that model is chosen.
    await router.waitForLog(
      /agentd: session settings key=\S+ catalog=openrouter-budget model=coding connection=opencode@openrouter adapter=opencode mode=\S* yolo=false effective=map\[effort:high mode:build model:openrouter\/deepseek\/deepseek-v4-pro-0813\]/,
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
    // A catalog pinning a model that is not on the adapter's list any more.
    const slot = (model: string) => ({ provider: 'opencode', connection: 'opencode@openrouter', model, effort: 'high' });
    writeFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), JSON.stringify({ catalogs: { stale: { name: 'Stale', slots: { frontier: slot('openrouter/nobody/imaginary-1'), coding: slot('openrouter/z-ai/glm-5.3'), small: slot('openrouter/z-ai/glm-5.3') } } } }));
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    const catalog = manager.locator('[data-testid="ai-catalog-select"]');
    await expect(catalog.locator('option[value="stale"]')).toHaveJSProperty('disabled', false, { timeout: 15_000 });
    await catalog.selectOption('stale');
    await manager.locator('[data-testid="ai-start"]').click();
    await expect(manager.locator('[data-testid="ai-start-error"]'))
      .toContainText('available values: opencode/big-pickle, openrouter/', { timeout: 25_000 });
  });
});
