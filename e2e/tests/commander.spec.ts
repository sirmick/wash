// Mission Commander's automatic briefs, end to end (docs/COMMANDER.md §5.3).
//
// Both halves. BE: with automatic mode on and an on-box provider (a local
// fake standing in for Ollama's OpenAI-compatible API), the commander
// observes the terminal through the router's verb on its cadence, briefs
// it through com.wash.inference in ONE batched request, and writes the
// brief to the journal; a tick that finds nothing changed sends nothing;
// a change brings one more brief. FE: the Timeline shows the brief as a
// row. And no log quotes the screen.

import { createServer, type Server } from 'node:http';
import type { AddressInfo } from 'node:net';
import { mkdirSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';

let server: Server;
let base = '';
let prompts: string[] = [];

test.beforeAll(async () => {
  server = createServer((req, res) => {
    let body = '';
    req.on('data', (c) => { body += c; });
    req.on('end', () => {
      const parsed = JSON.parse(body || '{}');
      const messages: Array<{ role?: string; content?: string }> = parsed.messages ?? [];
      const prompt = String(messages.find((m) => m.role === 'user')?.content ?? '');
      prompts.push(prompt);
      const n = prompts.length;
      // A batch carries "sources" and gets an array back; a single source
      // gets one brief. The goal names the call so a brief can be told
      // from the next.
      const batch = prompt.includes('"sources"');
      const content = batch
        ? JSON.stringify([{ index: 0, goal: `FAKE-GOAL-${n}`, state: 'active' }])
        : JSON.stringify({ goal: `FAKE-GOAL-${n}`, state: 'active' });
      res.writeHead(200, { 'Content-Type': 'application/json' });
      res.end(JSON.stringify({ choices: [{ message: { content } }], usage: { prompt_tokens: 9, completion_tokens: 5 } }));
    });
  });
  await new Promise<void>((r) => server.listen(0, '127.0.0.1', () => r()));
  // Loopback: the on-box rule lets automatic mode run without the hosted switch.
  base = `http://127.0.0.1:${(server.address() as AddressInfo).port}/v1`;
});
test.afterAll(() => server?.close());
test.beforeEach(() => { prompts = []; });

test.use({ routerOpts: { apps: ['session', 'inference', 'commander', 'term'] } });

/** setCommander is the rail's path: an app_msg to the session app's own BE,
 *  which forwards commander.set to the service as an attested send. */
async function setCommander(page: import('@playwright/test').Page, fields: Record<string, unknown>): Promise<void> {
  await page.evaluate((f) => {
    const inst = document.querySelector('wash-app-session')?.getAttribute('data-wash-instance') ?? '';
    window.wash.sendAppMsg(inst, { kind: 'commander_set', ...f });
  }, fields);
}

test('automatic briefs run on a cadence, batched, and only when something changed', async ({ page, router }) => {
  test.setTimeout(90_000);
  mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  writeFileSync(join(router.xdgConfigHome, 'wash', 'inference.json'), JSON.stringify({
    version: 1, default: 'ollama',
    connections: [{ id: 'ollama', name: 'Ollama', adapter: 'openai', base_url: base, model: 'fake-model' }],
  }));
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();

  // Off by default: the service is up (it autostarts) and does nothing.
  await expect(page.locator('wash-app-session')).toHaveAttribute('data-wash-instance', /./);
  expect(router.log()).toMatch(/wash-commander ready/);
  expect(router.log()).not.toMatch(/wash-commander: tick/);

  // Switch it on the way the rail does: through the session BE, which
  // forwards it attested. The shortest cadence the service honours.
  await setCommander(page, { automatic: true, interval_sec: 5, budget_per_hour: 100, batch_max: 6 });
  await expect.poll(() => router.log(), { timeout: 10_000 }).toMatch(/wash-commander: set automatic=true .*interval=5s .*by=com\.wash\.session/);

  // A terminal with something on it.
  await page.locator('button[title="Apps"]').click();
  await page.locator('[data-testid="start-menu"]').getByRole('button', { name: /^Terminal$/ }).click();
  const term = page.locator('wash-app-term');
  await expect(term).toBeVisible();
  await term.click();
  await expect(term).toContainText(/\$|#|>/, { timeout: 10_000 });
  await page.keyboard.type('echo BRIEF-$((40+2))-ME');
  await page.keyboard.press('Enter');
  await expect(term).toContainText('BRIEF-42-ME', { timeout: 10_000 });

  // BE half: within the cadence the terminal is observed by the commander
  // (attested), briefed once, and the brief lands in the journal.
  const briefs = () => page.evaluate(async () =>
    (await window.wash.activityQuery(undefined, { kinds: ['brief'] })).entries);
  await expect.poll(async () => (await briefs()).length, { timeout: 30_000 }).toBeGreaterThanOrEqual(1);
  const first = (await briefs())[0];
  expect(first.line).toContain('FAKE-GOAL-1');
  expect(first.app).toBe('com.wash.commander');
  expect(first.intent?.kind).toBe('focus');
  expect(first.intent?.app_id).toBe('com.wash.term');
  expect(first.ref?.source).toBe('pty-tail');

  expect(prompts.length).toBe(1);
  expect(prompts[0]).toContain('com.wash.term');
  expect(prompts[0]).toContain('BRIEF-42-ME');
  expect(router.log()).toMatch(/observe: instance=\S+ app=com\.wash\.term source=pty-tail bytes=\d+ by=com\.wash\.commander/);
  expect(router.log()).toMatch(/wash-commander: tick observed=1 changed=1 requests=1 briefs=1/);
  expect(router.log(), 'no log quotes the screen').not.toContain('BRIEF-42-ME');

  // FE half: the Timeline shows it.
  await page.locator('[data-testid="sidebar-section-header-timeline"]').click();
  await expect(page.locator('[data-testid="timeline-widget"]')).toContainText('FAKE-GOAL-1');

  // Nothing changed: the next ticks observe but send nothing.
  await expect.poll(() => (router.log().match(/wash-commander: tick observed=1 changed=0 requests=0/g) ?? []).length, { timeout: 30_000 }).toBeGreaterThanOrEqual(1);
  expect(prompts.length).toBe(1);
  expect((await briefs()).length).toBe(1);

  // Something changed: one more brief, one more request.
  await term.click();
  await page.keyboard.type('echo BRIEF-$((50+2))-ME');
  await page.keyboard.press('Enter');
  await expect(term).toContainText('BRIEF-52-ME', { timeout: 10_000 });
  await expect.poll(async () => (await briefs()).length, { timeout: 45_000 }).toBe(2);
  expect(prompts.length).toBe(2);
  expect(prompts[1]).toContain('BRIEF-52-ME');
  expect((await briefs())[0].line).toContain('FAKE-GOAL-2');
});

test('automatic mode is off by default and a hosted provider stays off without its switch', async ({ page, router }) => {
  mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  // A hosted-looking endpoint (not loopback) with automatic on: the
  // commander must not run.
  writeFileSync(join(router.xdgConfigHome, 'wash', 'inference.json'), JSON.stringify({
    version: 1, default: 'openai',
    connections: [{ id: 'openai', name: 'Hosted', adapter: 'openai', base_url: 'http://example.invalid/v1', model: 'm', api_key: 'k' }],
  }));
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  await expect(page.locator('wash-app-session')).toHaveAttribute('data-wash-instance', /./);
  await setCommander(page, { automatic: true, interval_sec: 5 });
  await expect.poll(() => router.log(), { timeout: 20_000 }).toMatch(/wash-commander: tick .*skipped="hosted provider/);
  expect(router.log()).not.toMatch(/observe: instance=/);
  expect(prompts.length).toBe(0);
});
