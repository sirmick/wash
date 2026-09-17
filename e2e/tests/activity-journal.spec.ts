// The activity journal and its Timeline (docs/COMMANDER.md §3, §6).
//
// Both halves. BE: the router writes what it witnessed to a JSONL day
// file under $XDG_STATE_HOME/wash/activity and answers the shell's
// activity.query with the same rows; an app's activity.note lands with the
// router-attested app id. FE: the sidebar Timeline lists those rows,
// grows live while it is open, and a row is a jump back to its window.

import { readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';
import type { RouterHandle } from '../fixtures/router';
import { AGENT_APPS, FAKE_DIR, startAgentSession } from '../fixtures/agents';

interface Row { ts: number; seq: number; host: string; kind: string; app?: string; window?: number; title?: string; line: string; intent?: { kind: string; window_id?: number; path?: string; session_id?: string } }

/** onDisk reads every row the router has written, oldest first. */
function onDisk(router: RouterHandle): Row[] {
  const dir = join(router.xdgStateHome, 'wash', 'activity');
  let files: string[] = [];
  try { files = readdirSync(dir).filter((f) => f.endsWith('.jsonl')).sort(); } catch { return []; }
  const rows: Row[] = [];
  for (const f of files) {
    for (const line of readFileSync(join(dir, f), 'utf8').split('\n')) {
      if (line.trim()) rows.push(JSON.parse(line) as Row);
    }
  }
  return rows;
}

/** viaShell asks the router the way the Timeline does. */
function viaShell(page: Page): Promise<Row[]> {
  return page.evaluate(async () => {
    const p = await window.wash.activityQuery(undefined, { limit: 200 });
    return p.entries as unknown[];
  }) as Promise<Row[]>;
}

/** closeWindow clicks a window's X and answers a close confirm if the app
 *  asks one (wash-term does). */
async function closeWindow(page: Page, app: ReturnType<Page['locator']>) {
  await page.locator('.wash-window', { has: app }).locator('[data-testid="window-close"]').click();
  const ok = page.locator('[data-testid="term-close-confirm-ok"]');
  try {
    await ok.waitFor({ state: 'visible', timeout: 1_500 });
    await ok.click();
  } catch {
    // no confirm: the window closed on the click
  }
}

/** openTimeline expands the sidebar's Timeline section. */
async function openTimeline(page: Page) {
  const header = page.locator('[data-testid="sidebar-section-header-timeline"]');
  await expect(header).toBeVisible();
  const body = page.locator('[data-testid="sidebar-section-body-timeline"]');
  if ((await body.count()) === 0) await header.click();
  await expect(body).toBeVisible();
  return body;
}

async function openTerminal(page: Page) {
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  return term;
}

test.describe('activity journal', () => {
  test.use({
    routerOpts: {
      apps: ['session', 'term', 'fm', 'edit'],
      fmRoot: true,
      fmSeed: (root: string) => writeFileSync(join(root, 'notes.md'), '# notes\n'),
    },
  });

  test('the router journals what it witnesses, on disk and over the shell verb', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await router.waitForLog(/activity: journal at .*wash\/activity/, 5_000);

    const term = await openTerminal(page);
    const winID = Number(await term.getAttribute('data-wash-window'));
    // Focus it explicitly (the launch focuses too; this makes a focus row
    // the test can point at), then close it.
    await term.click();
    await closeWindow(page, term);
    await expect(term).toHaveCount(0);

    // FE half: the shell verb returns the rows, newest first, with the
    // way back on each.
    await expect.poll(async () => (await viaShell(page)).map((r) => r.kind)).toEqual(
      expect.arrayContaining(['session.attach', 'window.open', 'window.focus', 'window.close']),
    );
    const rows = await viaShell(page);
    const open = rows.find((r) => r.kind === 'window.open' && r.app === 'com.wash.term');
    expect(open, 'a window.open row for the terminal').toBeTruthy();
    expect(open!.window).toBe(winID);
    expect(open!.intent).toMatchObject({ kind: 'focus', window_id: winID });
    expect(open!.host).toBe('local');
    for (let i = 1; i < rows.length; i++) {
      expect(rows[i - 1].ts >= rows[i].ts, 'newest first').toBe(true);
    }

    // BE half: the day file holds the same rows — (host, seq) identity,
    // and nothing beyond a line and a pointer.
    await expect.poll(() => onDisk(router).length).toBeGreaterThanOrEqual(rows.length);
    const disk = onDisk(router);
    const diskOpen = disk.find((r) => r.seq === open!.seq);
    expect(diskOpen).toMatchObject({ kind: 'window.open', app: 'com.wash.term', window: winID });
    expect(JSON.stringify(diskOpen).length).toBeLessThan(600);
    expect(disk.map((r) => r.seq)).toEqual([...disk.map((r) => r.seq)].sort((a, b) => a - b));
  });

  test('an open the router routes is journaled with its path, and a jump reopens it', async ({ page, router }) => {
    await page.goto(router.url);
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Files$/ }).click();
    const fm = page.locator('wash-app-fm');
    await expect(fm).toBeVisible();
    await expect(fm.locator('[data-testid="fm-entry-notes.md"]')).toBeVisible();
    const from = router.logCursor();
    await fm.locator('[data-testid="fm-entry-notes.md"]').dblclick();
    await router.waitForLog(/open\.routed: path="[^"]*notes\.md" app=com\.wash\.edit/, 15_000, from);
    await expect(page.locator('wash-app-edit')).toBeVisible();

    await expect.poll(async () => (await viaShell(page)).some((r) => r.kind === 'open.routed'), { timeout: 10_000 }).toBe(true);
    const row = (await viaShell(page)).find((r) => r.kind === 'open.routed')!;
    expect(row.app).toBe('com.wash.edit');
    expect(row.line).toContain('notes.md');
    expect(row.intent).toMatchObject({ kind: 'open', path: expect.stringContaining('notes.md') });
  });
});

test.describe('activity journal off', () => {
  test.use({ routerOpts: { apps: ['session', 'term'], extraArgs: ['--no-activity'] } });

  test('nothing is written and the shell is told', async ({ page, router }) => {
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    await openTerminal(page);
    const refused = await page.evaluate(() =>
      window.wash.activityQuery(undefined, { limit: 10 }).then(() => 'answered', (e: Error) => e.message));
    expect(refused).toContain('not_found');
    expect(onDisk(router)).toEqual([]);

    await openTimeline(page);
    await expect(page.locator('[data-testid="timeline-off"]')).toBeVisible();
  });
});

test.describe('timeline', () => {
  test.use({
    routerOpts: {
      apps: [...AGENT_APPS, 'term'],
      extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
    },
  });

  test('the sidebar Timeline lists rows, grows live, and a row is a jump', async ({ page, router }) => {
    test.setTimeout(60_000);
    await page.goto(router.url);
    await expect(page.locator('wash-app-session')).toBeVisible();
    const term = await openTerminal(page);
    const firstID = Number(await term.getAttribute('data-wash-window'));
    // Pinned by window id: a second terminal is about to open.
    const first = page.locator(`.wash-window:has(wash-app-term[data-wash-window="${firstID}"])`);
    const firstApp = page.locator(`wash-app-term[data-wash-window="${firstID}"]`);

    await openTimeline(page);
    const widget = page.locator('[data-testid="timeline-widget"]');
    await expect(widget).toBeVisible();
    // The rows the router already holds.
    await expect(widget.locator('[data-testid^="timeline-row-local-"][data-kind="window.open"]').first()).toBeVisible({ timeout: 10_000 });
    await expect(widget.locator('[data-testid="timeline-hour"]').first()).toContainText(/Today/);

    // Live: a second terminal, opened while the section is up, appears
    // through the tail — no reload, no re-query.
    const before = await widget.locator('[data-testid^="timeline-row-local-"]').count();
    await page.locator('button[title="Apps"]').click();
    await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Terminal', exact: true }).click();
    await expect(page.locator('wash-app-term')).toHaveCount(2);
    await expect.poll(() => widget.locator('[data-testid^="timeline-row-local-"]').count(), { timeout: 10_000 }).toBeGreaterThan(before);

    // A jump: minimize the first terminal, click its window.open row, and
    // it is back and focused.
    await page.evaluate((id) => window.wash.minimizeWindow(id), firstID);
    await expect(firstApp).toBeHidden();
    const rows = await viaShell(page);
    const openRow = rows.find((r) => r.kind === 'window.open' && r.window === firstID)!;
    const jump = router.logCursor();
    await widget.locator(`[data-testid="timeline-row-local-${openRow.seq}"]`).click();
    await expect(firstApp).toBeVisible({ timeout: 10_000 });
    // BE half of the jump: the router focused THAT window on the click.
    await router.waitForLog(new RegExp(`focus: win=${firstID} app=com\\.wash\\.term`), 10_000, jump);
    await expect(first).toBeVisible();

    // The Agents family: a fake agent session's start and turn are rows
    // with the router-attested app id — an app can only speak for itself.
    const from = router.logCursor();
    const win = await startAgentSession(page, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });
    await widget.locator('[data-testid="timeline-family-agents"]').click();
    await expect(widget.locator('[data-kind="agent.start"]').first()).toBeVisible({ timeout: 10_000 });
    await expect(widget.locator('[data-kind="agent.turn"]').first()).toBeVisible({ timeout: 20_000 });
    const agentRows = (await viaShell(page)).filter((r) => r.kind.startsWith('agent.'));
    expect(agentRows.length).toBeGreaterThanOrEqual(2);
    for (const r of agentRows) expect(r.app).toBe('com.wash.agentd');
    expect(agentRows.find((r) => r.kind === 'agent.start')?.intent).toMatchObject({ kind: 'resume' });
    // …and nothing in the router log says a note was refused.
    expect(router.log().slice(from)).not.toMatch(/activity: note refused/);

    // Clear removes the rows and leaves an audit line.
    const cleared = router.logCursor();
    await widget.locator('[data-testid="timeline-clear"]').click();
    await router.waitForLog(/activity: cleared by conn=\d+/, 10_000, cleared);
    await expect.poll(() => onDisk(router).length).toBe(0);
  });
});
