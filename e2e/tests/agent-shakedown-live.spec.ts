import { execSync } from 'node:child_process';
import { chmodSync, cpSync, existsSync, mkdirSync, mkdtempSync, readdirSync, readFileSync, writeFileSync } from 'node:fs';
import { homedir, tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from '../fixtures/router';

// The live shakedown: e2e/shakedown run by a real orchestrator (Claude
// Sonnet) with real members (Claude Haiku), in an isolated Wash, with this
// test as the owner: it answers the one question the script says the owner
// answers, and leaves the other unanswered. Costs real tokens, so it runs
// only when asked:
//
//   cd e2e && WASH_E2E_LIVE=1 pnpm exec playwright test tests/agent-shakedown-live.spec.ts
//
// It passes when check.sh passes; the orchestrator's report (its deviation
// log) is saved beside the test's results for reading, with a scorecard.
//
// WASH_E2E_CATALOG names a JSON file holding another catalog ({name, slots})
// to run it with instead, e.g. open-weight models on OpenRouter
// (apps/agentd/openrouter-eval/mixes/); the stored OpenRouter key is copied
// into the isolated Wash for it, and the scorecard carries the key's spend.
// WASH_E2E_MINUTES bounds the run (default 85).

test.skip(process.env.WASH_E2E_LIVE !== '1', 'live run: set WASH_E2E_LIVE=1');
test.use({ routerOpts: { apps: ['session', 'about', 'agentd', 'agents', 'ai', 'notify'] } });

const SHAKEDOWN = fileURLToPath(new URL('../shakedown', import.meta.url));

test('the shakedown, live', async ({ page, router }) => {
  const minutes = Number(process.env.WASH_E2E_MINUTES ?? 85);
  test.setTimeout((minutes + 5) * 60_000);
  const began = Date.now();
  const project = mkdtempSync(join(tmpdir(), 'wash-shakedown-live-'));
  cpSync(SHAKEDOWN, project, { recursive: true });
  execSync('git init -q && git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -qm "shakedown: start"', { cwd: project });
  mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  const slot = (model: string) => ({ provider: 'claude', model });
  const catalogFile = process.env.WASH_E2E_CATALOG;
  const catalog = catalogFile ? JSON.parse(readFileSync(catalogFile, 'utf8')) : { name: 'Shakedown', slots: { frontier: slot('sonnet'), coding: slot('sonnet'), small: slot('haiku') } };
  writeFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), JSON.stringify({ catalogs: { shakedown: catalog } }));
  let key = '';
  if (catalogFile) {
    const keys = join(process.env.XDG_CONFIG_HOME || join(homedir(), '.config'), 'wash', 'keys.json');
    key = JSON.parse(readFileSync(keys, 'utf8')).openrouter ?? '';
    cpSync(keys, join(router.xdgConfigHome, 'wash', 'keys.json'));
    chmodSync(join(router.xdgConfigHome, 'wash', 'keys.json'), 0o600);
  }
  const spend = async () => key ? (await (await fetch('https://openrouter.ai/api/v1/key', { headers: { Authorization: `Bearer ${key}` } })).json()).data.usage as number : 0;
  const spentBefore = await spend();
  console.log('shakedown catalog:', JSON.stringify(catalog.slots));
  console.log('shakedown project:', project);

  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: {
    kind: 'agent_start', claim: true, catalog: 'shakedown', model: 'frontier', cwd: project, yolo: true,
    prompt: 'Read SCRIPT.md and run the shakedown. Commit with git -c user.name=shakedown -c user.email=shakedown@localhost.',
  } });
  const app = page.locator('wash-app-ai');
  const stateFile = join(router.xdgStateHome, 'wash', 'workspaces.json');
  const state = () => existsSync(stateFile) ? JSON.parse(readFileSync(stateFile, 'utf8')) : { workspaces: [] };
  const report = test.info().outputPath('orchestrator.txt');
  let answered = false;
  const deadline = Date.now() + minutes * 60_000;
  // The owner: answer the gamma question in its asker's tab; never the
  // "Ready to end?" one (the script ends that member instead).
  let timedOut = false;
  // The owner also prods: when nothing has happened for three minutes and
  // the orchestrator's turn is over, it says to carry on, as a person
  // watching would. Each prod is scored; a mix that needs them is worse.
  const tdir = join(router.xdgStateHome, 'wash', 'agent-transcripts');
  const lastEvent = () => Math.max(0, ...(existsSync(tdir) ? readdirSync(tdir) : []).map(f => {
    const lines = readFileSync(join(tdir, f), 'utf8').trimEnd().split('\n');
    try { return JSON.parse(lines.at(-1) ?? '{}').at_ms ?? 0; } catch { return 0; }
  }));
  let prods = 0;
  for (;;) {
    // Out of time still scores: a mix that stalls is a result too.
    if (Date.now() > deadline) { timedOut = true; break; }
    const w = state().workspaces.findLast((x: any) => x.state !== 'ended');
    const gamma = w?.messages?.find((m: any) => m.type === 'decision_request' && m.delivery === 'recorded' && m.body.includes('gamma.txt'));
    if (gamma && !answered) {
      const sidebar = app.getByTestId('workspace-sidebar');
      await sidebar.getByTestId(`workspace-question-for-${gamma.id}`).click();
      const panel = app.getByTestId('question-panel');
      await panel.getByRole('radio', { name: /gamma/ }).first().click();
      const note = panel.getByRole('textbox').nth(1);
      if (await note.count()) await note.fill('lower case, like alpha and beta');
      await panel.getByRole('button', { name: /^Submit/ }).click();
      await expect(panel).toHaveCount(0);
      await app.getByRole('tab', { name: /^Conversation/ }).click();
      answered = true;
      console.log('owner answered', gamma.id);
    }
    const text = await app.getByTestId('agent-transcript').first().innerText().catch(() => '');
    writeFileSync(report, text);
    // Done when the orchestrator has run check.sh and its turn is over.
    const idle = !(await app.getByTestId('agent-stop').count());
    if (/(ok|FAIL) +A was accepted with trailers/.test(text) && idle) break;
    if (idle && prods < 5 && Date.now() - Math.max(lastEvent(), began) > 3 * 60_000) {
      const composer = app.locator('textarea').first();
      await composer.fill('Continue with the script.');
      await composer.press('Enter');
      prods++;
      console.log('owner prodded', prods);
    }
    await page.waitForTimeout(5_000);
  }
  const check = (() => { try { return execSync('./check.sh', { cwd: project, encoding: 'utf8' }); } catch (e: any) { return String(e.stdout ?? e); } })();
  writeFileSync(test.info().outputPath('check.txt'), check);
  console.log(check);
  // The scorecard: what a mix is judged on beside check.sh. Nudges are
  // Wash's reminders (a member that forgot to report, a node with nobody on
  // it), so fewer means the models kept to the process by themselves.
  const w = state().workspaces;
  const messages = w.flatMap((x: any) => x.messages ?? []);
  const count = (f: (m: any) => boolean) => messages.filter(f).length;
  await new Promise(r => setTimeout(r, 20_000)); // the key's usage lags
  // Pauses: stretches over five minutes in which no session recorded
  // anything (a sleeping laptop, a stalled provider). Counted apart, so a
  // mix's speed is its active time; the transcripts say which it was.
  const times = (existsSync(tdir) ? readdirSync(tdir) : []).flatMap(f => readFileSync(join(tdir, f), 'utf8').split('\n')
    .map(l => { try { return JSON.parse(l).at_ms as number; } catch { return undefined; } })
    .filter((t): t is number => typeof t === 'number' && t >= began)).sort((a, b) => a - b);
  const pauses = times.slice(1).map((t, i) => t - times[i]).filter(g => g > 5 * 60_000);
  const pauseMinutes = Math.round(pauses.reduce((a, b) => a + b, 0) / 600) / 100;
  const scorecard = {
    catalog: catalog.slots,
    timed_out: timedOut,
    owner_prods: prods,
    checks_ok: (check.match(/^ok /gm) ?? []).length,
    checks_failed: (check.match(/^FAIL /gm) ?? []).length,
    minutes: Math.round((Date.now() - began) / 600) / 100,
    pause_minutes: pauseMinutes,
    active_minutes: Math.round(((times.at(-1) ?? began) - began) / 600) / 100 - pauseMinutes,
    cost_usd: key ? Math.round(((await spend()) - spentBefore) * 10_000) / 10_000 : null,
    members: w.flatMap((x: any) => x.members ?? []).length,
    messages: messages.length,
    wash_reminders: count(m => m.sender === 'wash' && m.type !== 'note'),
    wash_notes: count(m => m.sender === 'wash' && m.type === 'note'),
    lifecycle: count(m => m.type === 'lifecycle'),
    failed_assignments: w.flatMap((x: any) => x.assignments ?? []).filter((a: any) => a.state === 'failed').length,
  };
  writeFileSync(test.info().outputPath('scorecard.json'), JSON.stringify(scorecard, null, 2));
  writeFileSync(test.info().outputPath('workspaces.json'), JSON.stringify(state(), null, 2));
  // Every session's transcript (tool calls, their input and output): the
  // record of what each member did, kept before the fixture removes the
  // state directory.
  if (existsSync(tdir)) cpSync(tdir, test.info().outputPath('transcripts'), { recursive: true });
  execSync(`git log --stat > ${JSON.stringify(test.info().outputPath('git-log.txt'))}; cp -a .wash ${JSON.stringify(test.info().outputPath('dot-wash'))}`, { cwd: project });
  console.log('scorecard:', JSON.stringify(scorecard));
  expect(timedOut, 'the shakedown did not finish in time').toBe(false);
  expect(check).not.toContain('FAIL');
});
