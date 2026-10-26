// The approval policy lives in the Agents window's Permissions tab and is
// saved through agentd (agent_set_policy, apps/agentd/be/policy.go). It used
// to be a Settings pane that wrote the whole of agents.json from a copy read
// when Settings opened, reverting whatever agentd had stored meanwhile.
//
//   1. Settings no longer has an Agents section.
//   2. The tab shows the stored policy; an edit saves the four policy fields
//      and leaves the rest of agents.json (here the launch default) alone.
//   3. A rule added outside the tab (what "always allow" does) shows up.

import { mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, openAgents } from '../fixtures/agents';

test.use({ routerOpts: { apps: [...AGENT_APPS, 'settings'] } });

const agentsJSON = (xdg: string) => join(xdg, 'wash', 'agents.json');
const readPolicy = (xdg: string) => JSON.parse(readFileSync(agentsJSON(xdg), 'utf8'));

test.describe('agent permissions', () => {
  test.setTimeout(60_000);

  test('Settings has no Agents section any more', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.settings' });
    const settings = page.locator('wash-app-settings');
    await expect(settings.getByRole('button', { name: 'Desktop', exact: true })).toBeVisible();
    await expect(settings.getByRole('button', { name: 'Agents', exact: true })).toHaveCount(0);
  });

  test('the Permissions tab edits the policy through agentd and keeps the rest of agents.json', async ({ page, router }) => {
    const xdg = router.xdgConfigHome;
    mkdirSync(join(xdg, 'wash'), { recursive: true });
    writeFileSync(
      agentsJSON(xdg),
      JSON.stringify({
        enabled: true,
        rules: [{ match: 'Read', decision: 'allow' }],
        launch: { mode: { codex: 'read-only' }, yolo: true },
      }),
    );

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const manager = await openAgents(page);
    await manager.locator('[data-testid="agents-tab-permissions"]').click();
    const tab = manager.locator('[data-testid="agents-permissions"]');
    await expect(tab.locator('[data-testid="agents-policy-enabled"]')).toBeChecked();
    await expect(tab.locator('[data-testid="agents-policy-rule-match"]')).toHaveValue('Read');

    // Add a rule, typed a character at a time like a person would.
    let cursor = router.logCursor();
    await tab.locator('[data-testid="agents-policy-rule-add"]').click();
    const match = tab.locator('[data-testid="agents-policy-rule-match"]').nth(1);
    await match.click();
    await page.keyboard.type('Bash(git status:*)');
    await expect(match).toBeFocused();
    await tab.locator('[data-testid="agents-policy-rule-decision"]').nth(1).selectOption('allow');
    await tab.locator('[data-testid="agents-policy-default"]').selectOption('deny');
    await router.waitForLog(/agentd: policy saved enabled=true default=deny rules=2/, 15_000, cursor);

    const file = readPolicy(xdg);
    expect(file.rules).toEqual([
      { match: 'Read', decision: 'allow' },
      { match: 'Bash(git status:*)', decision: 'allow' },
    ]);
    expect(file.default).toBe('deny');
    expect(file.launch).toEqual({ mode: { codex: 'read-only' }, yolo: true });

    // A rule added outside the tab, as "always allow" does, shows up
    // (agentd re-reads agents.json on its sweep).
    cursor = router.logCursor();
    writeFileSync(agentsJSON(xdg), JSON.stringify({ ...file, rules: [...file.rules, { match: 'WebFetch', decision: 'allow' }] }));
    await expect(tab.locator('[data-testid="agents-policy-rule-match"]')).toHaveCount(3, { timeout: 20_000 });
    await expect(tab.locator('[data-testid="agents-policy-rule-match"]').nth(2)).toHaveValue('WebFetch');
  });
});
