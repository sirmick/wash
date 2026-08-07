// Agent tabs in wash-edit (docs/AGENT_TABS.md).
//
// The editor hosts a coding-agent session in the pane its terminals already
// live in. Two things make that worth doing rather than pointing at the
// standalone Agent app:
//
//   - The folder is free. The editor knows which project is open, so
//     starting an agent asks nothing; the Agent app's first question is
//     always "which folder".
//   - The transcript and the buffer are on screen together, which is what
//     lets a tool row open the file it names in the editor above it. An
//     agent tab in the EDITOR pane could not do that — opening the file
//     would swap the transcript out.
//
// agentd owns the session either way, so this is a second host, not a
// second implementation.

import { fileURLToPath } from 'node:url';
import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'edit', 'agentd', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

test.describe('wash-edit agent tabs', () => {
  test.setTimeout(60_000);

  test('an agent runs in the editor pane, in the folder the editor has open', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-edit-agent-'));
    writeFileSync(join(dir, 'notes.md'), '# notes\n');

    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.edit' });
    const edit = page.locator('wash-app-edit').first();
    await expect(edit).toBeVisible();

    // Started from the Terminal menu, which works whether or not the pane
    // is already open — the ✦ button in the tab strip cannot, since the
    // strip only exists once the pane does.
    await edit.getByRole('button', { name: 'Terminal', exact: true }).click();
    await page.locator('[data-testid="edit-menu-agent-codex"]').click();

    // A tab appears immediately, with a composer once the session starts —
    // nobody was asked which folder.
    const composer = edit.locator('[data-testid="edit-term-pane"] [data-testid="agent-composer"]');
    await expect(composer).toBeVisible({ timeout: 30_000 });

    await composer.fill('say something');
    await composer.press('Enter');
    // The agent's reply lands in the pane, not in a separate window.
    await expect(edit.locator('[data-testid="edit-term-pane"]')).toContainText(
      'Hello from the fake agent.',
      { timeout: 30_000 },
    );
  });

  test('a terminal and an agent share the strip', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.edit' });
    const edit = page.locator('wash-app-edit').first();
    await expect(edit).toBeVisible();

    // A shell first — from the menu, since the strip's own + button lives
    // inside the pane it would be opening.
    await edit.getByRole('button', { name: 'Terminal', exact: true }).click();
    await page.locator('[data-testid="edit-menu-term-new"]').click();
    await expect(edit.locator('[data-testid="edit-term-tabs"] [data-testid^="edit-term-tab-"]:not([data-testid^="edit-term-tab-close"])')).toHaveCount(1);

    // …then an agent beside it. Both are a process working on your behalf
    // that you watch and interrupt, so they belong in one strip.
    // With the pane open, the ✦ button in the strip does the same thing.
    await edit.locator('[data-testid="edit-agent-new"]').click();
    await page.locator('[data-testid="edit-agent-start-codex"]').click();
    await expect(edit.locator('[data-testid="edit-term-tabs"] [data-testid^="edit-term-tab-"]:not([data-testid^="edit-term-tab-close"])')).toHaveCount(2);
    await expect(edit.locator('[data-testid="edit-term-pane"] [data-testid="agent-composer"]')).toBeVisible({ timeout: 30_000 });

    // Closing the agent tab leaves the terminal. The SESSION survives too —
    // agentd outlives its hosts — but that is asserted where it is visible,
    // on the roster, rather than here.
    const tabs = edit.locator('[data-testid="edit-term-tabs"] [data-testid^="edit-term-tab-close-"]');
    await tabs.last().click();
    await expect(edit.locator('[data-testid="edit-term-tabs"] [data-testid^="edit-term-tab-"]:not([data-testid^="edit-term-tab-close"])')).toHaveCount(1);
  });
});
