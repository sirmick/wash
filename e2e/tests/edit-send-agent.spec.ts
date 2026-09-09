// "Send to agent" from wash-edit (docs/Review-findings.md P2 → edit).
//
// The editor already hosts an agent session in its terminal pane, but
// there was no way to get the code you are looking at into it: you
// copied, switched pane, pasted, and typed the path from memory.
//
// Ctrl+Shift+Enter (and the editor's context menu) puts the selection —
// or the whole buffer when nothing is selected — into the agent tab's
// composer as a fenced block under the file's path, as a DRAFT: the
// point is to type the question next to it, not to fire the code off
// on its own.

import { fileURLToPath } from 'node:url';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

function seed(root: string): void {
  writeFileSync(join(root, 'calc.go'), 'package main\n\nfunc add(a, b int) int {\n\treturn a - b\n}\n');
}

test.use({
  routerOpts: {
    apps: ['session', 'edit', 'agentd', 'notify'],
    fmRoot: true,
    fmSeed: seed,
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

test.describe('wash-edit send to agent', () => {
  test.setTimeout(60_000);

  test('the selection lands in the agent composer as a fenced block', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.controlRequest({ t: 'launch', app_id: 'com.wash.edit' });
    const edit = page.locator('wash-app-edit').first();
    await expect(edit).toBeVisible();

    await edit.locator('[data-testid="edit-entry-calc.go"]').dblclick();
    await expect(edit.locator('.cm-content')).toContainText('func add');

    // Select the function body's one line.
    await edit.locator('.cm-content').click();
    await page.keyboard.press('Control+Home');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('ArrowDown');
    await page.keyboard.press('Shift+ArrowDown');

    // No agent session yet: sending starts one rather than refusing.
    await page.keyboard.press('Control+Shift+Enter');
    const composer = edit.locator('[data-testid="edit-term-pane"] [data-testid="agent-composer"]');
    await expect(composer).toBeVisible({ timeout: 30_000 });
    await expect.poll(() => composer.inputValue(), { timeout: 30_000 })
      .toContain(join(router.fmRoot, 'calc.go') + ':4');
    const draft = await composer.inputValue();
    expect(draft).toContain('```go');
    expect(draft).toContain('return a - b');
    // A draft, not a turn: nothing was sent.
    await expect(edit.locator('[data-testid="edit-term-pane"]')).not.toContainText('Hello from the fake agent.');

    // A second send appends under what is already there, and with no
    // selection it is the whole buffer.
    await edit.locator('.cm-content').click();
    await page.keyboard.press('Control+Home');
    await edit.locator('[data-testid="edit-menubar-terminal"]').click();
    await page.locator('[data-testid="edit-menu-send-agent"]').click();
    await expect.poll(() => composer.inputValue()).toContain('package main');
    expect((await composer.inputValue()).indexOf('```go')).toBeLessThan((await composer.inputValue()).lastIndexOf('```go'));

    // And it is an ordinary draft: Enter sends it.
    await composer.press('Enter');
    await expect(edit.locator('[data-testid="edit-term-pane"]')).toContainText(
      'Hello from the fake agent.',
      { timeout: 30_000 },
    );
  });
});
