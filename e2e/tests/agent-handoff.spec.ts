// Handing work between the Agent app and the rest of the desktop
// (docs/Review-findings.md P2 → agent: "'open in terminal' / 'send to
// agent' in either direction").
//
// Two directions, two mechanisms:
//
//   out — "Open terminal in project folder" spawns wash-term with the session's
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
//
// "Open terminal in project folder" is offered in two places — the session's row in the
// Agents manager's Running pane, and the controller's own Session menu —
// because the row naming a session and the window showing it are two views
// of one thing. Both are driven here: they are separate windows of the
// same binary, and either could lose the verb on its own.

import { mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS, 'term', 'fm', 'edit', 'test'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startAgentIn(page: Page, url: string, dir: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  return startAgentSession(page, undefined, { cwd: dir });
}

test.describe('agent handoff', () => {
  test.setTimeout(120_000);

  test('Open terminal in project folder on the Running row spawns wash-term with the session directory', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-term-'));
    await startAgentIn(page, router.url, dir);
    const row = page.locator('wash-app-agents [data-testid="agents-running-pane"] [data-testid^="agents-row-"]').first();
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

  test("Open terminal in project folder on the controller's Session menu does the same", async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-term-menu-'));
    const win = await startAgentIn(page, router.url, dir);

    const cursor = router.logCursor();
    await win.locator('[data-testid="ai-menubar-session"]').click();
    // Enabled only once the controller has its row's cwd — a click on a
    // disabled item would pass silently and fail on the log wait instead.
    const item = page.locator('[data-testid="ai-menu-open-terminal"]');
    await expect(item).toBeEnabled({ timeout: 15_000 });
    await item.click();

    await router.waitForLog(new RegExp(`wash-ai: open terminal cwd=${dir}$`, 'm'), 15_000, cursor);
    await expect(page.locator('wash-app-term').first()).toBeVisible({ timeout: 25_000 });
  });

  for (const surface of ['controller', 'manager'] as const) {
    for (const target of ['file-manager', 'text-editor'] as const) {
      test(`${surface} opens ${target} in the agent project folder`, async ({ page, router }) => {
        const dir = mkdtempSync(join(tmpdir(), 'wash-agent-project-'));
        writeFileSync(join(dir, 'project-note.txt'), 'Project shortcut fixture.');
        const win = await startAgentIn(page, router.url, dir);
        if (surface === 'controller') await win.getByTestId('ai-menubar-session').click();
        else await page.locator('wash-app-agents [data-testid="agents-running-pane"] [data-testid="agents-verbs-btn"]').first().click();
        const action = page.getByTestId(`${surface === 'controller' ? 'ai' : 'agents'}-menu-open-${target}`);
        await expect(action).toBeEnabled();
        await action.click();
        if (target === 'file-manager') {
          const files = page.locator('wash-app-fm');
          await expect(files.getByTestId('fm-path')).toHaveValue(dir);
          await expect(files.getByText('project-note.txt', { exact: true })).toBeVisible();
        } else {
          const editor = page.locator('wash-app-edit');
          await expect(editor.getByTestId('edit-sidebar')).toContainText(dir);
          await expect(editor.getByTestId('edit-sidebar').getByText('project-note.txt', { exact: true })).toBeVisible();
        }
      });
    }
  }

  test('an agent_draft message from another app lands in the composer, unsent', async ({ page, router }) => {
    const dir = mkdtempSync(join(tmpdir(), 'wash-agent-draft-'));
    const win = await startAgentIn(page, router.url, dir);
    const composer = win.locator('textarea');
    await composer.fill('what does this do? ');

    // The sender is a DIFFERENT app, with a router-attested identity —
    // the path wash-edit will use, not a payload claiming to be one.
    const inst = await win.getAttribute('data-wash-instance');
    expect(inst).toBeTruthy();
    // The sender is wash-test, which exists only in a TEST_APP=1 build —
    // the layout the e2e suite runs against.
    const launched = await router.controlRequest({ t: 'launch', app_id: 'com.wash.test' });
    expect(launched.t).toBe('launched');
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
