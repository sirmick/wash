import { fileURLToPath } from 'node:url';
import { existsSync, mkdirSync, readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { test, expect } from '../fixtures/router';
import { freshHistory } from '../fixtures/agents';

// The shakedown (e2e/shakedown/SCRIPT.md), deterministic: the spec is the
// orchestrator, typing tool calls; fake members follow keywords in their
// tasks. Every step exercises one feature of the plan, the questions and
// the signals, and checks what the owner and the orchestrator see.

const FAKE_DIR = fileURLToPath(new URL('../../out/e2e', import.meta.url));
test.use({ routerOpts: { apps: ['session', 'about', 'agentd', 'agents', 'ai', 'notify'], extraEnv: { PATH: `${FAKE_DIR}:${process.env.PATH ?? ''}`, WASH_FAKE_WORKSPACE: '1' } } });

const workspaceToml = `name = "Shakedown"
max_active = 4
max_members = 8
qa_dir = ".wash/qa"
plan_file = ".wash/plan.toml"
legend = "🧪 in review; 🔴 blocked on the owner"

[roles.implementer]
instructions = "You write one line in one file. Report with member_update."
`;

test('the plan, its questions and its signals, step by step', async ({ page, router }) => {
  test.setTimeout(180_000);
  const project = join(router.xdgConfigHome, 'shakedown');
  mkdirSync(join(project, '.wash'), { recursive: true });
  writeFileSync(join(project, '.wash', 'workspace.toml'), workspaceToml);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: { kind: 'agent_start', claim: true, agent: 'codex', cwd: project, prompt: '' } });
  const app = page.locator('wash-app-ai');
  const composer = app.locator('[data-testid="agent-composer"]').first();
  const outputs = app.locator('[data-testid="agent-transcript"]').first().locator('pre');
  const sidebar = app.getByTestId('workspace-sidebar');
  await expect(composer).toBeEnabled();
  const tool = async (name: string, args: object = {}, error = false) => {
    const tab = app.getByRole('tab', { name: /^Conversation/ });
    if (await tab.count()) await tab.click();
    const count = await outputs.count();
    await composer.fill(`workspace ${name} ${JSON.stringify(args)}`); await composer.press('Enter');
    await expect(outputs).toHaveCount(count + 1);
    const text = await outputs.last().innerText();
    expect(text).toMatch(error ? /^WORKSPACE_ERROR / : /^WORKSPACE_RESULT /);
    return error ? text : JSON.parse(text.slice('WORKSPACE_RESULT '.length));
  };
  const state = () => JSON.parse(readFileSync(join(router.xdgStateHome, 'wash/workspaces.json'), 'utf8')).workspaces.at(-1);
  const node = (id: string) => state().plan.find((n: any) => n.id === id);
  const member = (key: string) => state().members.find((m: any) => m.key === key);
  const planTab = () => app.getByRole('tab', { name: 'Plan' });

  // 1. The workspace comes from its file; the plan starts as three sketches.
  await tool('workspace_configure', { from: '.wash/workspace.toml' });
  await expect(sidebar).toBeVisible();
  expect(state().name).toBe('Shakedown');
  await tool('plan_set', { nodes: {
    M1: { title: 'Plan', template: 'milestone', state: 'active' },
    M2: { title: 'Build', template: 'milestone', needs: ['M1'] },
    M3: { title: 'Ship', template: 'milestone', needs: ['M2'] },
  } });
  await planTab().click();
  await expect(app.getByTestId('workspace-plan-legend')).toContainText('in review');
  await expect(app.getByTestId('workspace-plan-node-M3')).toContainText('Sketch');

  // 2. The Plan milestone expands Build into three nodes; C needs A and B.
  await tool('plan_set', { nodes: {
    M1: { state: 'done' },
    A: { title: 'alpha.txt', parent: 'M2', body: 'words/alpha.txt says alpha' },
    B: { title: 'beta.txt', parent: 'M2' },
    C: { title: 'gamma.txt', parent: 'M2', needs: ['A', 'B'] },
  } });
  await planTab().click();
  await expect(app.getByTestId('workspace-plan-edge-A-C')).toHaveCount(1);
  await expect(app.getByTestId('workspace-plan-edge-B-C')).toHaveCount(1);
  expect(readFileSync(join(project, '.wash', 'plan.toml'), 'utf8')).toContain('parent = "M2"');

  // 3. Members on their nodes; A's task is written at once, B asks A's
  // implementer through QA and leaves work running in the background.
  await tool('workspace_configure', { members: {
    'a-impl': { name: 'Implementer', node: 'A', role: 'implementer', lifetime: 'resident', instructions: 'Write alpha.', task: 'Write words/alpha.txt' },
    'a-red': { name: 'Red team', node: 'A', role: 'reviewer', lifetime: 'resident', instructions: 'Review alpha.' },
    'b-impl': { name: 'Implementer', node: 'B', role: 'implementer', lifetime: 'resident', instructions: 'Write beta.', task: 'ASK_PEER a-impl A-case A BACKGROUND_WORK' },
    'c-impl': { name: 'Implementer', node: 'C', role: 'implementer', lifetime: 'resident', instructions: 'Write gamma.' },
  } });
  expect(member('a-impl').instructions).toContain('You write one line in one file.');
  await expect.poll(() => node('A').state).toBe('reported');
  const bImpl = () => member('b-impl').id;
  await expect(sidebar.getByTestId(`workspace-activity-label-${bImpl()}`)).toHaveText('Background', { timeout: 20_000 });
  await expect.poll(() => node('B').state).toBe('reported');
  await expect.poll(() => existsSync(join(project, '.wash', 'qa', 'A-case.md'))).toBe(true);
  expect(readFileSync(join(project, '.wash', 'qa', 'A-case.md'), 'utf8')).toContain('Is alpha lower case?');
  await expect(sidebar.getByTestId(`workspace-activity-label-${bImpl()}`)).not.toHaveText('Background', { timeout: 20_000 });

  // 4. C needs A and B done: refused, then started with a recorded reason.
  expect(await tool('assignment_update', { updates: [{ action: 'create', member_id: 'c-impl', text: 'ASK_OWNER' }] }, true)).toContain('override');
  await tool('assignment_update', { updates: [{ action: 'create', member_id: 'c-impl', text: 'ASK_OWNER', override: 'the shakedown asks the owner early' }] });
  expect(node('C').overrides[0]).toContain('the shakedown asks the owner early');

  // 5. C's implementer asks the owner: the question blocks it, needs you
  // everywhere, and is answered in its tab.
  await expect.poll(() => state().messages.some((m: any) => m.type === 'decision_request' && m.delivery === 'recorded')).toBe(true);
  const decision = state().messages.find((m: any) => m.type === 'decision_request' && m.delivery === 'recorded').id;
  const cImpl = member('c-impl').id;
  await planTab().click();
  await expect(app.getByTestId('workspace-plan-needs-you-C')).toBeVisible();
  await sidebar.getByTestId(`workspace-question-for-${decision}`).click();
  await expect(app.getByRole('tab', { name: /C · Implementer ●/ })).toHaveAttribute('aria-selected', 'true');
  await app.getByTestId('question-option-word-0').click();
  await app.getByTestId('question-text-note').fill('lower case, like alpha');
  await app.getByTestId(`question-submit-${decision}`).click();
  await expect.poll(() => state().assignments.find((a: any) => a.member_id === cImpl && a.state === 'completed')?.result ?? '').toContain('→ gamma');
  expect(state().assignments.find((a: any) => a.member_id === cImpl && a.state === 'completed').result).toContain('→ lower case, like alpha');
  await expect.poll(() => node('C').state).toBe('reported');
  await expect(app.getByTestId('question-panel')).toHaveCount(0);

  // 6. A review round, created and waited on in one call; A is accepted with
  // its evidence as commit trailers.
  const round = await tool('assignment_update', { updates: [{ action: 'create', member_id: 'a-red', text: 'Review alpha' }], wait: { reason: 'A review' } });
  expect(round.waiting_on).toHaveLength(1);
  await expect.poll(() => state().assignments.find((a: any) => a.id === round.waiting_on[0]).state).toBe('completed');
  await tool('member_update', { qa_updates: [{ id: 'A-case', action: 'resolve', expected_revision: state().qa.find((q: any) => q.id === 'A-case').revision, evidence: 'alpha.txt is lower case' }] });
  const accepted = await tool('plan_accept', { node: 'A', gates: [{ command: 'grep -q alpha words/alpha.txt', exit_code: 0 }] });
  expect(accepted.trailers).toContain('Plan-Node: A');
  expect(accepted.trailers).toContain('QA: A-case');
  expect(accepted.trailers).toContain('Reviewed-by: Red team: Fixture completed');
  expect(accepted.stage).toEqual(['.wash/plan.toml', '.wash/qa/A-case.md']);

  // 7. A member fails on purpose; the node says so.
  await tool('assignment_update', { updates: [{ action: 'create', member_id: 'b-impl', text: 'FAIL_ON_PURPOSE' }] });
  await expect.poll(() => node('B').state).toBe('failed');
  await tool('plan_set', { nodes: { B: { state: 'done' } } });

  // 8. Ending a member with its node active: the orchestrator hears it.
  await tool('assignment_update', { updates: [{ action: 'create', member_id: 'c-impl', text: 'ASK_OWNER' }] });
  await expect.poll(() => state().messages.filter((m: any) => m.type === 'decision_request' && m.delivery === 'recorded').length).toBe(1);
  await tool('member_control', { action: 'end', member_ids: ['c-impl'] });
  await expect.poll(() => state().messages.some((m: any) => m.sender === 'wash' && m.body.includes('Node C (gamma.txt) is active with nobody on it'))).toBe(true);
  await expect(app.getByTestId('question-panel')).toHaveCount(0);

  // 9. C done completes Build; Build done points at Ship, still a sketch.
  await tool('plan_set', { nodes: { C: { state: 'done' } } });
  await expect.poll(() => state().messages.some((m: any) => m.sender === 'wash' && m.body.includes('Every node in milestone M2'))).toBe(true);
  await tool('plan_set', { nodes: { M2: { state: 'done' }, M3: { state: 'active' } } });
  await expect.poll(() => state().messages.some((m: any) => m.sender === 'wash' && m.body.includes('M3 (Ship) is next, still a sketch'))).toBe(true);

  // 10. Status comes from the plan.
  const plan = await tool('plan_get');
  expect(plan.nodes).toEqual(expect.arrayContaining(['M3 · active · Ship [milestone]', 'M2 · done · Build [milestone]']));
  expect(plan.nodes.some((l: string) => l.startsWith('  A · done · alpha.txt · on: Implementer'))).toBe(true);

  // 11. Ending with Ship active is a decision; a new workspace resumes the
  // plan and QA from the project's files.
  expect(await tool('workspace_end', {}, true)).toContain('M3');
  await tool('workspace_end', { confirm: true });
  await expect(sidebar).toHaveCount(0);
  await tool('workspace_configure', { from: '.wash/workspace.toml' });
  await expect.poll(() => state().plan.length).toBe(6);
  expect(node('M3').state).toBe('active');
  expect(state().qa.find((q: any) => q.id === 'A-case')).toMatchObject({ state: 'resolved', archived: true, resumed: true });
  const thread = await tool('workspace_get', { view: 'qa', thread_id: 'A-case' });
  expect(thread.thread.events.some((e: any) => e.body.includes('Is alpha lower case?'))).toBe(true);
  await tool('workspace_end', { confirm: true });
});

// Outside a workspace too: Claude Code's AskUserQuestion arrives as a form,
// is answered in the panel above the composer, and reaches the agent; work
// left running in the background shows on the roster until it ends.
test('an adapter question is answered in the panel, and background work shows', async ({ page, router }) => {
  test.setTimeout(90_000);
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: { kind: 'agent_start', claim: true, agent: 'codex', cwd: router.xdgConfigHome, prompt: '' } });
  const app = page.locator('wash-app-ai');
  const composer = app.locator('[data-testid="agent-composer"]').first();
  await expect(composer).toBeEnabled();
  await composer.fill('please elicit'); await composer.press('Enter');
  const panel = app.getByTestId('question-panel');
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('Which clock source?');
  await expect(panel).toContainText('Never goes back');
  await panel.getByRole('radio', { name: /Monotonic/ }).click();
  await panel.getByRole('checkbox', { name: /rv32/ }).click();
  await panel.getByRole('checkbox', { name: /x86/ }).click();
  await panel.getByLabel('Your answer: Which clock source?').fill('RAW if there is one');
  await panel.getByRole('button', { name: /^Submit/ }).click();
  await expect(panel).toHaveCount(0);
  // The content the agent got back (the transcript renders it as Markdown).
  const transcript = app.getByTestId('agent-transcript');
  await expect(transcript).toContainText('ELICIT<<accept');
  await expect(transcript).toContainText('"Monotonic"');
  await expect(transcript).toContainText('"RAW if there is one"');
  await expect(transcript).toContainText('["rv32","x86"]');
  await composer.fill('run it in the background'); await composer.press('Enter');
  await expect(app.getByTestId('agent-transcript')).toContainText('Started in the background.');
  await expect(app.getByText(/background · make bench/).first()).toBeVisible({ timeout: 10_000 });
  await expect(app.getByText(/background · make bench/)).toHaveCount(0, { timeout: 15_000 });
});

// The Redoubt hang and what now prevents and reports it. A member whose
// agent wakes itself (a background task finished) is not sent mail until it
// is idle again: the fake, like claude-agent-acp, swallows a prompt sent
// into that turn. A turn that never ends is reported by the supervisor, and
// an interrupt frees the member after the cancel deadline.
test('a member\'s own turn holds its mail; a hung turn is reported and freed', async ({ page, router }) => {
  test.setTimeout(150_000);
  const project = join(router.xdgConfigHome, 'stuck');
  mkdirSync(project, { recursive: true });
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: { kind: 'agent_start', claim: true, agent: 'codex', cwd: project, prompt: '' } });
  const app = page.locator('wash-app-ai');
  const composer = app.locator('[data-testid="agent-composer"]').first();
  const outputs = app.locator('[data-testid="agent-transcript"]').first().locator('pre');
  await expect(composer).toBeEnabled();
  const tool = async (name: string, args: object = {}) => {
    const tab = app.getByRole('tab', { name: /^Conversation/ });
    if (await tab.count()) await tab.click();
    const count = await outputs.count();
    await composer.fill(`workspace ${name} ${JSON.stringify(args)}`); await composer.press('Enter');
    await expect(outputs).toHaveCount(count + 1, { timeout: 30_000 });
    const text = await outputs.last().innerText();
    expect(text).toMatch(/^WORKSPACE_RESULT /);
    return JSON.parse(text.slice('WORKSPACE_RESULT '.length));
  };
  const state = () => JSON.parse(readFileSync(join(router.xdgStateHome, 'wash/workspaces.json'), 'utf8')).workspaces.at(-1);
  const member = (key: string) => state().members.find((m: any) => m.key === key);
  const message = (id: string) => state().messages.find((m: any) => m.id === id);

  await tool('workspace_configure', { workspace: { name: 'Stuck', project_root: project }, supervisor: { quiet: '10s' } });
  await tool('plan_set', { nodes: { S: { title: 'Self' }, H: { title: 'Hang' } } });

  // The agent's own turn: mail waits for its idle, then goes and is answered.
  await tool('workspace_configure', { members: { self: { name: 'Self', node: 'S', lifetime: 'resident', instructions: 'Work.', task: 'SELF_TURN' } } });
  await expect.poll(() => state().plan.find((n: any) => n.id === 'S').state).toBe('reported');
  const selfId = member('self').id;
  await expect.poll(async () => (await tool('workspace_get', { view: 'state' })).activity[selfId], { timeout: 10_000, intervals: [100] }).toMatch(/^(working|responding|thinking)$/);
  const ping = await tool('message_send', { recipient: 'self', type: 'instruction', body: 'PING' });
  expect(message(ping.id).delivery).toBe('queued');
  await expect.poll(() => message(ping.id).delivery, { timeout: 20_000 }).toBe('delivered');

  // A turn that never ends: the supervisor reports it, the interrupt frees it.
  await tool('workspace_configure', { members: { hang: { name: 'Hang', node: 'H', lifetime: 'resident', instructions: 'Work.', task: 'HANG_TURN' } } });
  await expect.poll(() => state().messages.some((m: any) => m.sender === 'wash' && m.body.includes('wedged: Hang (hang)')), { timeout: 45_000 }).toBe(true);
  const freed = await tool('member_control', { action: 'interrupt', member_ids: ['hang'] });
  expect(freed.outcomes[0].result.abandoned).toBe(true);
  const after = await tool('message_send', { recipient: 'hang', type: 'instruction', body: 'PING' });
  await expect.poll(() => message(after.id).delivery, { timeout: 20_000 }).toBe('delivered');

  // History lists the orchestrator's conversation, not its members, until
  // asked; then the members sit under it.
  const lead = state().members.find((m: any) => m.id === state().orchestrator).session_id;
  const members = [member('self').session_id, member('hang').session_id];
  await tool('workspace_end', { confirm: true });
  const history = await freshHistory(page);
  const row = (sid: string) => history.locator(`[data-testid="ai-history-row"][data-session-id="${sid}"]`);
  await expect(row(lead)).toBeVisible({ timeout: 15_000 });
  for (const sid of members) await expect(row(sid)).toHaveCount(0);
  await history.getByTestId('ai-history-all').check();
  for (const sid of members) await expect(row(sid)).toHaveAttribute('data-depth', '1', { timeout: 15_000 });
  await expect(row(lead)).toHaveAttribute('data-depth', '0');
});

// A member's assignment and result wrap to the pane, and the pane scrolls
// when they are longer than it: a long line, an unbroken token or a wide
// code block must not push the pane sideways past the window.
test('the assignment pane wraps and scrolls', async ({ page, router }) => {
  test.setTimeout(90_000);
  const project = join(router.xdgConfigHome, 'wrap');
  mkdirSync(project, { recursive: true });
  await page.goto(router.url);
  await expect(page.locator('wash-app-session')).toBeVisible();
  const started = await router.controlRequest({ t: 'launch', app_id: 'com.wash.ai' });
  await router.controlRequest({ t: 'msg', instance_id: String(started.instance_id), data: { kind: 'agent_start', claim: true, agent: 'codex', cwd: project, prompt: '' } });
  const app = page.locator('wash-app-ai');
  const composer = app.locator('[data-testid="agent-composer"]').first();
  const outputs = app.locator('[data-testid="agent-transcript"]').first().locator('pre');
  await expect(composer).toBeEnabled();
  const tool = async (name: string, args: object = {}) => {
    const tab = app.getByRole('tab', { name: /^Conversation/ });
    if (await tab.count()) await tab.click();
    const count = await outputs.count();
    await composer.fill(`workspace ${name} ${JSON.stringify(args)}`); await composer.press('Enter');
    await expect(outputs).toHaveCount(count + 1, { timeout: 30_000 });
  };
  const state = () => JSON.parse(readFileSync(join(router.xdgStateHome, 'wash/workspaces.json'), 'utf8')).workspaces.at(-1);

  const task = [
    'A long paragraph: ' + 'words that should wrap at the pane edge '.repeat(60),
    'An unbroken token: ' + 'x'.repeat(600),
    '```',
    'a code line ' + '0123456789'.repeat(60),
    '```',
    ...Array.from({ length: 80 }, (_, i) => `- step ${i}`),
  ].join('\n');
  await tool('workspace_configure', { workspace: { name: 'Wrap', project_root: project } });
  await tool('plan_set', { nodes: { W: { title: 'Wrap' } } });
  await tool('workspace_configure', { members: { w: { name: 'Wrapper', node: 'W', lifetime: 'resident', instructions: 'Work.', task } } });
  await expect.poll(() => state().plan.find((n: any) => n.id === 'W').state).toBe('reported');
  await app.getByTestId(`workspace-member-${state().members.find((m: any) => m.key === 'w').id}`).click();

  const brief = app.getByTestId('workspace-member-brief');
  await expect(brief).toContainText('A long paragraph');
  const box = await brief.evaluate((el) => ({ scrollWidth: el.scrollWidth, clientWidth: el.clientWidth, scrollHeight: el.scrollHeight, clientHeight: el.clientHeight, right: el.getBoundingClientRect().right }));
  const panel = await app.getByTestId('workspace-member-detail').evaluate((el) => el.getBoundingClientRect().right);
  expect(box.right, 'the pane stays inside the member panel').toBeLessThanOrEqual(panel + 1);
  expect(box.scrollWidth, 'nothing pushes the pane sideways').toBeLessThanOrEqual(box.clientWidth + 1);
  expect(box.scrollHeight, 'the brief is longer than the pane').toBeGreaterThan(box.clientHeight);
  await brief.hover();
  await page.mouse.wheel(0, 600);
  await expect.poll(() => brief.evaluate((el) => el.scrollTop)).toBeGreaterThan(0);
  await tool('workspace_end', { confirm: true });
});
