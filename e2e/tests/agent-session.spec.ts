// The managed agent path, end to end (docs/AGENT_APP.md §13).
//
// M5 deleted six specs that covered the intercept tier and shipped
// nothing in their place; these are the replacement.
//
// The agent is `e2e/fixtures/acp-fake`, built as `codex-acp` and put on a
// PATH this test controls — so nothing in production knows it is a
// fixture, and the suite needs no API key, no network and no money. Its
// frames were captured from real adapters with the conformance tracer,
// and it passes the same conformance test they do; a shape that drifts
// upstream fails there first.

import { fileURLToPath } from 'node:url';
import type { Page } from '@playwright/test';
import { test, expect } from '../fixtures/router';

// Resolved from this file, not from process.cwd(): playwright can be
// invoked from the repo root or from e2e/, and a cwd-relative path
// silently found NO fake — so the suite quietly ran the real agent, cost
// real tokens, and failed on text a model had actually written.
const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));

test.use({
  routerOpts: {
    apps: ['session', 'agentd', 'ai', 'notify'],
    extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}` },
  },
});

// openAgent opens the desktop and starts a session in a fresh Agent
// window. The fake is the only adapter on this PATH, so "Start" starts it.
async function openAgent(page: Page, url: string, prompt: string) {
  await page.goto(url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: 'Agent', exact: true }).click();
  const win = page.locator('wash-app-ai').first();
  await expect(win).toBeVisible();

  await win.locator('select').selectOption('codex');
  await win.getByRole('button', { name: 'Start session' }).click();

  const composer = win.locator('textarea');
  await expect(composer).toBeVisible({ timeout: 20_000 });
  await composer.fill(prompt);
  await composer.press('Enter');
  return win;
}

test.describe('managed agent sessions', () => {
  test('a session streams its reply into the transcript', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');

    // What the human typed comes back as a transcript line — an agent
    // does not echo the prompt its own client just sent it, so wash
    // records its side.
    await expect(win.getByText('say something')).toBeVisible({ timeout: 20_000 });

    // Markdown, not a literal ## and asterisks.
    await expect(win.getByText('Heading', { exact: true })).toBeVisible({ timeout: 20_000 });

    // Streamed across three chunks that split mid-sentence. This is the
    // assertion that catches reordering and lost whitespace — the two
    // bugs that actually happened.
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible();

    // A tool call is one line, not a wall of output.
    await expect(win.getByText('README.md')).toBeVisible();

    // A GFM table becomes a real table, not a row of pipes.
    await expect(win.locator('table')).toBeVisible();
    await expect(win.locator('th', { hasText: 'Adapter' })).toBeVisible();
    await expect(win.locator('td', { hasText: 'claude' })).toBeVisible();

    // An inline image is DECODED, not merely present: a broken <img>
    // would still satisfy toBeVisible, which is the trap here.
    const img = win.locator('img[alt="image from the agent"]');
    await expect(img).toBeVisible();
    await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.naturalWidth)).toBeGreaterThan(0);
  });

  test('a permission request can be answered, and the answer reaches the agent', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'please ask before running');

    // The command the agent wants to run is what the human reads.
    await expect(win.getByText('echo hello > /tmp/wash-e2e-fake')).toBeVisible({ timeout: 20_000 });

    await win.getByRole('button', { name: /^Allow(\s|$)/ }).first().click();

    // The fake reports back what outcome it received, so this asserts the
    // answer reached the AGENT — not merely that a button disappeared.
    await expect(win.getByText('Permission outcome: allow')).toBeVisible({ timeout: 20_000 });
  });

  test('closing a session window asks what to do with the agent', async ({ page, router }) => {
    const win = await openAgent(page, router.url, 'say something');
    await expect(win.getByText('Hello from the fake agent.')).toBeVisible({ timeout: 20_000 });

    // The window's own close button, i.e. the path a user takes.
    await page.locator('[data-testid="window-close"]').first().click();

    // Three outcomes, because dismissing must not silently pick a
    // destructive one.
    const dialog = page.locator('[data-testid="ai-close-confirm"]');
    await expect(dialog).toBeVisible({ timeout: 10_000 });
    await expect(dialog.getByRole('button', { name: 'Detach' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Terminate' })).toBeVisible();
    await expect(dialog.getByRole('button', { name: 'Keep open' })).toBeVisible();
  });
});
