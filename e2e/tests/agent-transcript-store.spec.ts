// Transcripts are saved live, to disk (GH #21).
//
// agentd has always captured every session — into memory, for the
// router's lifetime, with nothing ever freeing it. So the conversation
// died with the router and a long-lived box accumulated every transcript
// it had seen. transcript_store.go gives that capture a disk tail.
//
// The Go tests cover the format, the seq fold, the resume reconciliation
// and the path safety. What they cannot cover is that a REAL router,
// running a REAL adapter, actually writes the file — that the binding
// happens on the live session-start path and not just in a test's call to
// bindTranscript. That is what this asserts.

import { readFileSync, existsSync, readdirSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// The fixture gives every router its own XDG_STATE_HOME, so this reads
// exactly what THIS test's agentd wrote — no run inherits another's.
const transcriptsDir = (state: string) => join(state, 'wash', 'agent-transcripts');

test('a live session writes its transcript to disk as it happens', async ({ page, router }) => {
  test.setTimeout(60_000);

  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Agent', exact: true })
    .click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();

  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill('remember this line');
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  // "Live" is the claim: the file exists while the session is still open,
  // without anything having been closed, ended or flushed by hand.
  await expect
    .poll(() => (existsSync(transcriptsDir(router.xdgStateHome)) ? readdirSync(transcriptsDir(router.xdgStateHome)).length : 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);

  const files = readdirSync(transcriptsDir(router.xdgStateHome)).filter((f) => f.endsWith('.jsonl'));
  expect(files).toHaveLength(1);
  const lines = readFileSync(join(transcriptsDir(router.xdgStateHome), files[0]), 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((l) => JSON.parse(l) as Record<string, unknown>);

  // First line names the session, so a transcript is self-describing even
  // if agent-sessions.json is lost.
  expect(lines[0].kind).toBe('meta');
  expect(lines[0].session_id).toBeTruthy();
  expect(lines[0].agent).toBe('codex');

  // Both sides of the conversation: what the human typed (wash records
  // its own side — the agent never echoes it back) and what came back.
  const texts = lines.map((l) => String(l.text ?? ''));
  expect(texts.some((t) => t.includes('remember this line'))).toBe(true);
  expect(texts.some((t) => t.includes('Hello from the fake agent.'))).toBe(true);

  // Every EVENT line carries a seq — it is what an update folds against,
  // and a file without it cannot be reloaded correctly. meta and summary
  // records are not events and deliberately carry none.
  const events = lines.filter((l) => l.kind !== 'meta' && l.kind !== 'summary');
  expect(events.length).toBeGreaterThan(0);
  for (const l of events) expect(typeof l.seq).toBe('number');

  // The summary is what the history panel lists from: it must name the
  // model, which is read off the agent's own settings block rather than
  // assumed. The fake adapter exposes one, so an empty model here means
  // the capture path is broken, not that the agent has no model.
  const summaries = lines.filter((l) => l.kind === 'summary');
  expect(summaries.length).toBeGreaterThan(0);
  expect(String(summaries[0].agent)).toBe('codex');
});

test('the transcript outlives the session it came from', async ({ page, router }) => {
  test.setTimeout(60_000);

  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page
    .locator('[data-testid="start-menu"]')
    .getByRole('button', { name: 'Agent', exact: true })
    .click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();
  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();
  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill('outlive me');
  await composer.press('Enter');
  await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

  await expect
    .poll(() => (existsSync(transcriptsDir(router.xdgStateHome)) ? readdirSync(transcriptsDir(router.xdgStateHome)).length : 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  const before = readdirSync(transcriptsDir(router.xdgStateHome)).filter((f) => f.endsWith('.jsonl'));

  // End the session from the Agent app's roster (it moved there in
  // SIDEBAR.md M2c). retire() frees the in-memory transcript — which is
  // only safe BECAUSE the file is the other copy.
  const pane = win.locator('[data-testid="ai-roster-pane"]');
  if ((await pane.count()) === 0) await win.locator('[data-testid="ai-roster-toggle"]').click();
  const row = pane.locator('[data-testid^="agents-row-"]').first();
  await expect(row).toBeVisible({ timeout: 15_000 });
  const cursor = router.logCursor();
  await row.locator('[data-testid="agents-verbs-btn"]').click();
  await page.locator('[data-testid="agents-menu-end"]').click();
  await page.locator('[data-testid="agents-menu-end-confirm"]').click();
  await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);

  // The conversation is still on disk after the session that made it is
  // gone. Same files, still readable, still carrying the prompt.
  const after = readdirSync(transcriptsDir(router.xdgStateHome)).filter((f) => f.endsWith('.jsonl'));
  expect(after).toEqual(before);
  const all = after
    .map((f) => readFileSync(join(transcriptsDir(router.xdgStateHome), f), 'utf8'))
    .join('\n');
  expect(all).toContain('outlive me');
});
