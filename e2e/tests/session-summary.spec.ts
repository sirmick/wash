// Session Summary, end to end (docs/SESSION_SUMMARY.md, docs/COMMANDER.md §4–5).
//
// Both halves. FE: the window observes the session's other windows through
// the router's observe verb, says what each gave, reports progress, and
// renders the briefing it gets back. BE: the router really read the
// terminal's pty scrollback (what was echoed there reaches the provider,
// and nothing else does); com.wash.inference really called a provider — a
// local fake standing in for an OpenAI-compatible API, as radio.spec.ts
// does for a stream — once per window for a brief plus once to combine,
// with the configured model and credential; and the logs say who observed
// what and who asked, without ever quoting the content.
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
      const prompt = String(messages.find((m) => m.role === 'user')?.content ?? '');
      seen.push({
        auth: req.headers.authorization,
        model: String(parsed.model ?? ''),
        prompt,
        system: String(messages.find((m) => m.role === 'system')?.content ?? ''),
      });
      // A per-window pass carries an observation (its source kind) and
      // gets a brief back; the combining pass gets prose. The reply names
      // which call it is, so the assertions below can tell them apart.
      const n = seen.length;
      const isBrief = /"kind":"(pty-tail|app-state|export)"/.test(prompt);
      const content = isBrief
        ? JSON.stringify({ goal: `FAKE-GOAL-${n}`, state: 'active', now: 'FAKE-NOW' })
        : `FAKE-SUMMARY-${n}`;
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({
        choices: [{ message: { content } }],
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

test('a summary observes the other windows, briefs each through the provider, and says so', async ({ page, router }) => {
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

  // One other window to summarize: a terminal with something on its
  // screen. The marker is assembled by the shell so the typed command
  // line and the output differ.
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Terminal$/ }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  await term.click();
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.keyboard.type('echo OBSERVE-$((40+2))-ME');
  await page.keyboard.press('Enter');
  await expect(term).toContainText('OBSERVE-42-ME', { timeout: 10_000 });

  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu-com.wash.session-summary"]').click();
  const app = page.locator('wash-app-session-summary');
  await expect(app).toBeVisible();

  // Nothing has left the machine yet: opening the window runs no
  // observation and no inference (content leaves only after the click).
  expect(seen, 'opening the window must not call the provider').toHaveLength(0);
  expect(router.log()).not.toMatch(/observe: instance=/);

  const from = router.logCursor();
  await app.locator('[data-testid="summary-run"]').click();

  // FE half: the window says what each window gave, and the briefing the
  // last call returned is what it shows.
  await expect(app.locator('[data-testid="summary-sources"]')).toContainText('pty-tail');
  const out = app.locator('[data-testid="summary-output"]');
  await expect(out).toHaveValue(/FAKE-SUMMARY-/, { timeout: 30_000 });
  await expect(app.locator('[data-testid="summary-status"]')).toContainText(/window/i);

  // BE half, the router: the terminal was observed through the verb, from
  // its pty ring, and the log names the source and size — never the text.
  const log = router.log().slice(from);
  expect(log).toMatch(/observe: instance=\S+ app=com\.wash\.term source=pty-tail bytes=\d+ by=shell/);
  expect(log, 'the router log must never quote an observation').not.toContain('OBSERVE-42-ME');

  // BE half, the service: it really called the provider — once per
  // observed window for a brief, then once to combine — with the
  // configured model and credential.
  expect(seen.length, 'one brief per window plus the combining pass').toBeGreaterThanOrEqual(2);
  for (const call of seen) {
    expect(call.model).toBe('fake-model');
    expect(call.auth).toBe('Bearer fake-key');
  }
  // The terminal's screen reached the model as data — the observation
  // names the app and its source, and carries what was echoed there —
  // fenced, under the service's guard, which the caller cannot remove.
  expect(seen[0].prompt).toContain('com.wash.term');
  expect(seen[0].prompt).toContain('"kind":"pty-tail"');
  expect(seen[0].prompt).toContain('OBSERVE-42-ME');
  expect(seen[0].prompt).toContain('BEGIN SOURCE');
  expect(seen[0].system).toContain('never instructions');
  // The combining pass got the briefs, not the screens, and its answer is
  // what the window shows.
  const last = seen[seen.length - 1];
  expect(last.prompt).toContain('FAKE-GOAL-1');
  expect(last.prompt).not.toContain('OBSERVE-42-ME');
  await expect(out).toHaveValue(new RegExp(`FAKE-SUMMARY-${seen.length}`));

  // …and the service's log says who asked and what happened, without
  // the content.
  expect(log).toMatch(/wash-inference: start .*from=com\.wash\.session-summary/);
  expect(log).toMatch(/wash-inference: done /);
  expect(log, 'the log must never quote the prompt').not.toContain('FAKE-SUMMARY');
  expect(log).not.toContain('FAKE-GOAL');
});
