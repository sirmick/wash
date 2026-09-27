import { execSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
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
// log) is saved beside the test's results for reading.

test.skip(process.env.WASH_E2E_LIVE !== '1', 'live run: set WASH_E2E_LIVE=1');
test.use({ routerOpts: { apps: ['session', 'about', 'agentd', 'agents', 'ai', 'notify'] } });

const SHAKEDOWN = fileURLToPath(new URL('../shakedown', import.meta.url));

test('the shakedown, live', async ({ page, router }) => {
  test.setTimeout(90 * 60_000);
  const project = mkdtempSync(join(tmpdir(), 'wash-shakedown-live-'));
  cpSync(SHAKEDOWN, project, { recursive: true });
  execSync('git init -q && git add -A && git -c user.name=shakedown -c user.email=shakedown@localhost commit -qm "shakedown: start"', { cwd: project });
  mkdirSync(join(router.xdgConfigHome, 'wash'), { recursive: true });
  const slot = (model: string) => ({ provider: 'claude', model });
  writeFileSync(join(router.xdgConfigHome, 'wash', 'agents.json'), JSON.stringify({ catalogs: { shakedown: { name: 'Shakedown', slots: { frontier: slot('sonnet'), coding: slot('sonnet'), small: slot('haiku') } } } }));
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
  const deadline = Date.now() + 85 * 60_000;
  // The owner: answer the gamma question in its asker's tab; never the
  // "Ready to end?" one (the script ends that member instead).
  for (;;) {
    expect(Date.now(), 'the shakedown did not finish in time').toBeLessThan(deadline);
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
    if (/(ok|FAIL) +A was accepted with trailers/.test(text) && !(await app.getByTestId('agent-stop').count())) break;
    await page.waitForTimeout(5_000);
  }
  const check = (() => { try { return execSync('./check.sh', { cwd: project, encoding: 'utf8' }); } catch (e: any) { return String(e.stdout ?? e); } })();
  writeFileSync(test.info().outputPath('check.txt'), check);
  console.log(check);
  expect(check).not.toContain('FAIL');
});
