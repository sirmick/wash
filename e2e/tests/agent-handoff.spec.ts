// Handing work between the Agent app and the rest of the desktop
// (docs/Review-findings.md P2 → agent: "'open in terminal' / 'send to
// agent' in either direction").
//
// Two directions, two mechanisms:
//
//   out — "Open terminal here" spawns wash-term with the session's
//         working directory, because where the agent is working is
//         exactly where a person wants a shell;
//   in  — an `agent_draft` app message puts text in the composer. Any app
//         may send it (wash-edit's "send selection to agent" is written
//         against this exact shape), and it is a DRAFT: it lands at the
//         caret and waits, because what a person does with a selection —
//         frame it with a question, trim it, think better of it — is the
//         whole reason it goes to a composer.
//
// The argv the term spawn carries (`--open <dir>`) is asserted here as
// far as this side can see it: wash-ai's own log names the confined
// directory it asked the router for, and a terminal window appears.
// wash-term learning to START in that directory is the term track's half.

import { fileURLToPath } from 'node:url';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'term', 'test', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startAgentIn(page: Page, url: string, dir: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();

  await win.getByRole('button', { name: 'Choose…' }).click();
  const picker = page.locator('[data-testid="ai-folder-picker"]');
  await expect(picker).toBeVisible();
  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(dir);
  await bar.press('Enter');
  await picker.locator('[data-testid="fp-confirm"]').click();
  await expect(picker).toBeHidden();

  await win.locator('select').first().selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  await expect(win.locator('textarea')).toBeVisible({ timeout: 20_000 });
  return win;
}

test.describe('agent handoff', () => {
  test.setTimeout(120_000);

  test('Open terminal here spawns wash-term with the session directory', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-term-'));
    const win = await startAgentIn(page, router.url, dir);
    const row = win.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]').first();
    await expect(row).toBeVisible({ timeout: 15_000 });

    const cursor = router.logCursor();
    await row.locator('[data-testid="agents-verbs-btn"]').click();
    await page.locator('[data-testid="agents-menu-open-terminal"]').click();

    // The directory wash-ai asked the router to launch the terminal with,
    // confined before it was sent — the FE named the path, and a window
    // is not authority for one.
    await router.waitForLog(new RegExp(`wash-ai: open terminal cwd=${dir}$`, 'm'), 15_000, cursor);
    await expect(page.locator('wash-app-term').first()).toBeVisible({ timeout: 25_000 });
  });

  test('an agent_draft message from another app lands in the composer, unsent', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-draft-'));
    const win = await startAgentIn(page, router.url, dir);
    const composer = win.locator('textarea');
    await composer.fill('what does this do? ');

    // The sender is a DIFFERENT app, with a router-attested identity —
    // the path wash-edit will use, not a payload claiming to be one.
    const inst = await win.getAttribute('data-wash-instance');
    expect(inst).toBeTruthy();
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    const testInst = launched.instance_id as string;
    const cursor = router.logCursor();
    await router.controlRequest({
      t: 'msg',
      instance_id: testInst,
      data: {
        kind: 'send_to',
        target_inst: inst,
        payload: { kind: 'agent_draft', text: 'func Confine(p string) (string, error)' },
      },
    });
    await router.waitForLog(/wash-ai: draft from=com\.wash\.test bytes=\d+/, 15_000, cursor);

    // At the caret, and NOT sent: the composer still holds it.
    await expect(composer).toHaveValue('what does this do? func Confine(p string) (string, error)', { timeout: 15_000 });
    expect(router.log().slice(cursor)).not.toMatch(/acp prompt key=/);
  });
});
