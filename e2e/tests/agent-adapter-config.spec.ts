// Adapter configuration in ~/.config/wash/agents.json (P2 → agent:
// "custom adapter command/args/env, MCP servers (always [])").
//
// wash had no opinion about how an adapter was started: a hardcoded name
// on PATH, a hardcoded arg list, whatever environment the router happened
// to inherit, and `mcpServers` on session/new was literally always `[]` —
// so an agent under wash could reach no MCP server at all, however many
// the same agent reached from a terminal.
//
// The merge itself is unit-tested (internal/agentpolicy). What only an
// e2e can show is that the merged launch is the one that actually ran, so
// the fake adapter reports its own argv and environment back into the
// transcript (`launchinfo`).

import { fileURLToPath } from 'node:url';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

function writeAgentsJSON(configHome: string, body: unknown) {
  const dir = join(configHome, 'wash');
  mkdirSync(dir, { recursive: true });
  writeFileSync(join(dir, 'agents.json'), JSON.stringify(body, null, 2));
}

async function startAgent(page: Page, url: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').first().selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  await expect(win.locator('textarea')).toBeVisible({ timeout: 20_000 });
  return win;
}

test.describe('adapter configuration', () => {
  test.setTimeout(90_000);

  test('extra args and env from agents.json reach the spawned adapter', async ({ page, router }) => {
    writeAgentsJSON(router.xdgConfigHome, {
      agents: {
        codex: {
          args: ['--fake-extra'],
          env: { WASH_FAKE_MARK: 'from-agents-json' },
        },
      },
    });

    const win = await startAgent(page, router.url);
    const composer = win.locator('textarea');
    await composer.fill('launchinfo');
    await composer.press('Enter');

    // The adapter's own report of how it was started.
    await expect(win.getByText(/LAUNCH<<args=--fake-extra mark=from-agents-json>>/)).toBeVisible({ timeout: 20_000 });
  });

  test('MCP servers configured for an agent are offered on session/new', async ({ page, router }) => {
    writeAgentsJSON(router.xdgConfigHome, {
      mcp_servers: [{ name: 'fs', command: 'mcp-fs', args: ['--root', '/w'] }],
      agents: { codex: { mcp_servers: [{ name: 'repo', command: 'mcp-repo', env: { TOKEN: 't' } }] } },
    });

    const cursor = router.logCursor();
    await startAgent(page, router.url);
    // The fake logs every frame it could not handle, but session/new it
    // answers — so the wire is asserted where wash writes it: agentd's
    // own line, which now counts what it offered.
    await router.waitForLog(/agentd: acp session started .*mcp=2/, 25_000, cursor);
  });

  test('no agents.json is the launch every box had before', async ({ page, router }) => {
    const cursor = router.logCursor();
    const win = await startAgent(page, router.url);
    const composer = win.locator('textarea');
    await composer.fill('launchinfo');
    await composer.press('Enter');
    await expect(win.getByText(/LAUNCH<<args= mark=>>/)).toBeVisible({ timeout: 20_000 });
    await router.waitForLog(/agentd: acp session started .*mcp=0/, 25_000, cursor);
  });
});
