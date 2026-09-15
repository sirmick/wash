// Per-session roots beyond the cwd (docs/Review-findings.md P2 → agent:
// "fs/terminal confined to the session cwd with no override — monorepo
// sibling dirs unreadable").
//
// The cwd is the scope a person consented to, and it is the right
// default. It was the wrong LIMIT: reading a sibling package meant
// starting the agent at the parent, granting far more than the two
// folders it needed. A session now carries a set of roots, both
// confinements read it, and the status bar names what was added — because
// the hazard of widening a session is forgetting that you did.

import { mkdirSync, mkdtempSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import type { Locator, Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, openAgents, startAgentSession, windowOf } from '../fixtures/agents';

test.use({
  routerOpts: {
    apps: [...AGENT_APPS],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

async function startAgentIn(page: Page, url: string, dir: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  return startAgentSession(page, undefined, { cwd: dir });
}

// Widening a session is a roster verb, and the roster lives in the Agents
// manager's Running pane — the controller window renders only the session.
// So the verb is driven there, and its effect (the chip, the reads) is
// asserted in the controller. The manager is raised first because the
// controller agentd just opened may be sitting on top of it.
async function addRoot(page: Page, dir: string): Promise<Locator> {
  const manager = await openAgents(page);
  const row = manager.locator('[data-testid="ai-roster-pane"] [data-testid^="agents-row-"]').first();
  await expect(row).toBeVisible({ timeout: 15_000 });
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  await page.locator('[data-testid="agents-menu-add-root"]').click();
  const picker = page.locator('[data-testid="ai-root-picker"]');
  await expect(picker).toBeVisible();
  const bar = picker.locator('[data-testid="fp-path"]');
  await bar.click();
  await bar.fill(dir);
  await bar.press('Enter');
  await picker.locator('[data-testid="fp-confirm"]').click();
  await expect(picker).toBeHidden();
  return row;
}

const ask = async (win: ReturnType<Page['locator']>, text: string) => {
  const composer = win.locator('textarea');
  await composer.fill(text);
  await composer.press('Enter');
};

test.describe('per-session roots', () => {
  test.setTimeout(120_000);

  test('a sibling folder becomes readable once allowed, and closes again when taken back', async ({ page, router }) => {
    const base = mkdtempSync(join(tmpdir(), 'wash-agent-roots-'));
    const work = join(base, 'app');
    const sib = join(base, 'lib');
    mkdirSync(work);
    mkdirSync(sib);
    const shared = join(sib, 'schema.json');
    writeFileSync(shared, 'SIBLING-CONTENT\n');

    const win = await startAgentIn(page, router.url, work);

    // Before: outside every root. With nobody having allowed it, the read
    // raises a question — which is the point: it used to fail silently.
    await ask(win, `readfile ${shared}`);
    const askRow = win.getByRole('button', { name: /^Deny/ }).first();
    await expect(askRow).toBeVisible({ timeout: 20_000 });
    await askRow.click();
    await expect(win.getByText(/READ<<REFUSED/)).toBeVisible({ timeout: 20_000 });

    // Allow the sibling from the manager's roster row menu.
    const row = await addRoot(page, sib);

    // Named in the status bar, not counted.
    const chip = win.locator('[data-testid="agent-root"]');
    await expect(chip).toHaveAttribute('data-path', sib, { timeout: 15_000 });
    await expect(chip).toContainText('lib');

    // After: the same read goes through, with no question at all.
    await ask(win, `readfile ${shared}`);
    await expect(win.getByText(/READ<<SIBLING-CONTENT/)).toBeVisible({ timeout: 20_000 });

    // Taking it back closes the folder again — from the controller's own
    // status bar. Activating the row raises the one controller rather than
    // opening another; it is maximized because agentd opens it cascaded
    // below the manager, which puts its status bar under the taskbar.
    await row.click();
    await windowOf(page, win).locator('[data-testid="window-maximize"]').click();
    await win.locator('[data-testid="agent-root-remove"]').click();
    await expect(win.locator('[data-testid="agent-root"]')).toHaveCount(0, { timeout: 15_000 });
    await ask(win, `readfile ${shared}`);
    await expect(win.getByRole('button', { name: /^Deny/ }).first()).toBeVisible({ timeout: 20_000 });
  });

  test('the terminal confinement honours the same roots', async ({ page, router }) => {
    const base = mkdtempSync(join(tmpdir(), 'wash-agent-roots-term-'));
    const work = join(base, 'app');
    const sib = join(base, 'lib');
    mkdirSync(work);
    mkdirSync(sib);
    writeFileSync(join(sib, 'marker'), '');

    const win = await startAgentIn(page, router.url, work);

    await addRoot(page, sib);
    await expect(win.locator('[data-testid="agent-root"]')).toHaveCount(1, { timeout: 15_000 });

    // A folder an agent may read and may not run anything in is a
    // distinction nobody asked for: the write half uses the same roots.
    await ask(win, `writefile ${join(sib, 'made.txt')} ok`);
    await expect(win.getByText('WROTE<<OK>>')).toBeVisible({ timeout: 20_000 });
  });
});
