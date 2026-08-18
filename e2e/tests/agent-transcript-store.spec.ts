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

import { mkdtempSync, readFileSync, existsSync, readdirSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from '../fixtures/router';

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

// The router (and so agentd) writes under XDG_STATE_HOME. Pointing it at
// a throwaway dir keeps the suite off the developer's real history — the
// host-state leakage that has bitten these specs before.
const STATE_DIR = mkdtempSync(join(tmpdir(), 'wash-agent-state-'));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: {
      PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`,
      XDG_STATE_HOME: STATE_DIR,
    },
  },
});

const transcriptsDir = () => join(STATE_DIR, 'wash', 'agent-transcripts');

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
    .poll(() => (existsSync(transcriptsDir()) ? readdirSync(transcriptsDir()).length : 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);

  const files = readdirSync(transcriptsDir()).filter((f) => f.endsWith('.jsonl'));
  expect(files).toHaveLength(1);
  const lines = readFileSync(join(transcriptsDir(), files[0]), 'utf8')
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
  const texts = lines.slice(1).map((l) => String(l.text ?? ''));
  expect(texts.some((t) => t.includes('remember this line'))).toBe(true);
  expect(texts.some((t) => t.includes('Hello from the fake agent.'))).toBe(true);

  // Every event line carries a seq — it is what an update folds against,
  // and a file without it cannot be reloaded correctly.
  for (const l of lines.slice(1)) expect(typeof l.seq).toBe('number');
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
    .poll(() => (existsSync(transcriptsDir()) ? readdirSync(transcriptsDir()).length : 0), {
      timeout: 15_000,
    })
    .toBeGreaterThan(0);
  const before = readdirSync(transcriptsDir()).filter((f) => f.endsWith('.jsonl'));

  // End the session from the rail. retire() now frees the in-memory
  // transcript — which is only safe BECAUSE the file is the other copy.
  const header = page.locator('[data-testid="sidebar-section-header-agents"]');
  const body = page.locator('[data-testid="sidebar-section-body-agents"]');
  if ((await body.count()) === 0) await header.click();
  const row = body.locator('[data-testid^="agents-row-"]').first();
  await expect(row).toBeVisible({ timeout: 15_000 });
  const cursor = router.logCursor();
  await row.locator('[data-testid="agents-row-menu"]').click();
  await page.locator('[data-testid="agents-menu-end"]').click();
  await page.locator('[data-testid="agents-menu-end-confirm"]').click();
  await router.waitForLog(/agentd: acp session ended key=/, 15_000, cursor);

  // The conversation is still on disk after the session that made it is
  // gone. Same files, still readable, still carrying the prompt.
  const after = readdirSync(transcriptsDir()).filter((f) => f.endsWith('.jsonl'));
  expect(after).toEqual(before);
  const all = after
    .map((f) => readFileSync(join(transcriptsDir(), f), 'utf8'))
    .join('\n');
  expect(all).toContain('outlive me');
});
