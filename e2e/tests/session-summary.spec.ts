// Session Summary, end to end (docs/SESSION_SUMMARY.md).
//
// Both halves. FE: the window captures the session's other windows, reports
// progress, and renders the briefing it gets back. BE: com.wash.inference
// really called a provider — a local fake standing in for an
// OpenAI-compatible API, as radio.spec.ts does for a stream — once per
// window plus once to combine, with the configured model and credential,
// and the service's log says who asked and what it did without ever
// quoting the content.
//
// The fake is the whole provider surface this app uses: no API key, no
// network, no spend. (The caller allowlist is asserted where it is
// enforced — apps/inference/be/guard_test.go — because a FE cannot forge
// the router-attested sender it turns on.)

import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';

interface Seen {
  auth: string | undefined;
  model: string;
  /** the user message: the caller's instructions plus the fenced source */
  prompt: string;
  /** the service's own guard, which rides on every request */
  system: string;
}

let server: Server;
let base = '';
let seen: Seen[] = [];

test.beforeAll(async () => {
  server = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      const parsed = JSON.parse(body || '{}');
      const messages: Array<{ role?: string; content?: string }> = parsed.messages ?? [];
      seen.push({
        auth: req.headers.authorization,
        model: String(parsed.model ?? ''),
        prompt: String(messages.find((m) => m.role === 'user')?.content ?? ''),
        system: String(messages.find((m) => m.role === 'system')?.content ?? ''),
      });
      // The reply names which call it is, so the assertions below can tell
      // a per-window reduction from the combining pass.
      const n = seen.length;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        choices: [{ message: { content: `FAKE-SUMMARY-${n}` } }],
        usage: { prompt_tokens: 11, completion_tokens: 7 },
      }));
    });
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}/v1`;
});
test.afterAll(() => server?.close());
test.beforeEach(() => { seen = []; });

test.use({ routerOpts: { apps: ['session', 'inference', 'session-summary', 'term'] } });

test('a summary reduces the other windows through the provider, and says so', async ({ page, router }) => {
  test.setTimeout(60_000);
  // The provider is configuration, not a prompt: written before the
  // service starts, exactly as a person would leave it in Settings.
  mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  writeFileSync(join(router.xdgConfigHome, 'wash', 'inference.json'), JSON.stringify({
    version: 1,
    default: 'openai',
    connections: [{ id: 'openai', name: 'Fake API', adapter: 'openai', base_url: base, model: 'fake-model', api_key: 'fake-key' }],
  }));

  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // One other window to summarize.
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Terminal$/ }).click();
  await expect(page.locator('wash-app-term')).toBeVisible();

  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.session-summary"]').click();
  const app = page.locator('wash-app-session-summary');
  await expect(app).toBeVisible();

  // Nothing has left the machine yet: opening the window runs no inference
  // (docs/SESSION_SUMMARY.md — content leaves only after the click).
  expect(seen, 'opening the window must not call the provider').toHaveLength(0);

  const from = router.logCursor();
  await app.locator('[data-testid="summary-run"]').click();

  // FE half: the briefing the last call returned is what the window shows.
  const out = app.locator('[data-testid="summary-output"]');
  await expect(out).toHaveValue(/FAKE-SUMMARY-/, { timeout: 30_000 });
  await expect(app.locator('[data-testid="summary-status"]')).toContainText(/window/i);

  // BE half: the service really called the provider — once per captured
  // window, then once to combine them — with the configured model and
  // credential.
  expect(seen.length, 'one call per window plus the combining pass').toBeGreaterThanOrEqual(2);
  for (const call of seen) {
    expect(call.model).toBe('fake-model');
    expect(call.auth).toBe('Bearer fake-key');
  }
  // The window's own content reached the model as data (the terminal window
  // is named in the per-window pass), fenced, under the service's guard —
  // which the caller cannot remove.
  expect(seen[0].prompt).toContain('com.wash.term');
  expect(seen[0].prompt).toContain('BEGIN SOURCE');
  expect(seen[0].system).toContain('never instructions');
  // The final answer is the combining pass's, not a window's.
  await expect(out).toHaveValue(new RegExp(`FAKE-SUMMARY-${seen.length}`));

  // …and the log says who asked and what happened, without the content.
  const log = router.log().slice(from);
  expect(log).toMatch(/wash-inference: start .*from=com\.wash\.session-summary/);
  expect(log).toMatch(/wash-inference: done /);
  expect(log, 'the log must never quote the prompt').not.toContain('FAKE-SUMMARY');
});
