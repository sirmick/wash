// Component test (Tier B) for the coding-agent roster widget — the pure
// renderer of com.wash.agentd's snapshot. Covers empty/populated
// rendering, the state language (colour + label), the "dir · branch*"
// place line, and the click-to-focus callback. The cross-process flow (a
// real agent in a real terminal reaching the sidebar) is
// term-agent-roster.spec.ts; that's the integration seam.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { AgentsWidget, fmtAgo, fmtElapsed, stateColor, stateLabel, type AgentAsk, type AgentRow, type AgentSession } from './AgentsWidget.tsx';

afterEach(cleanup);

const row = (over: Partial<AgentRow> = {}): AgentRow => ({
  key: 'i-1:5',
  agent: 'claude',
  state: 'working',
  term_instance: 'i-1',
  window_id: 1,
  channel_id: 5,
  since_ms: 0,
  ...over,
});

const noop = () => {};
const at = (_key: string) => 0;

test('empty: says so rather than rendering an empty box', () => {
  const { queryByTestId } = render(() => (
    <AgentsWidget rows={() => []} startedAt={at} now={() => 0} onFocus={noop} />
  ));
  expect(queryByTestId('agents-empty')).toBeTruthy();
});

test('populated: one row per agent, state on the element for the chrome to read', () => {
  const rows = [row({ key: 'a', state: 'needs-input', reason: 'permission' }), row({ key: 'b', agent: 'codex' })];
  const { queryByTestId, getByTestId } = render(() => (
    <AgentsWidget rows={() => rows} startedAt={at} now={() => 0} onFocus={noop} />
  ));
  expect(queryByTestId('agents-empty')).toBeNull();
  expect(getByTestId('agents-row-a').getAttribute('data-agent-state')).toBe('needs-input');
  expect(getByTestId('agents-row-a').getAttribute('data-agent')).toBe('claude');
  expect(getByTestId('agents-row-b').getAttribute('data-agent')).toBe('codex');
  // The reason is what makes "needs input" actionable.
  expect(getByTestId('agents-row-a').textContent).toContain('needs input · permission');
});

test('place line: repo, branch, and a star when the tree is dirty', () => {
  const rows = [
    row({ key: 'clean', dir: 'wash', branch: 'main' }),
    row({ key: 'dirty', dir: 'wash', branch: 'main', dirty: true }),
    row({ key: 'nogit', dir: 'tmp' }),
  ];
  const { getByTestId } = render(() => (
    <AgentsWidget rows={() => rows} startedAt={at} now={() => 0} onFocus={noop} />
  ));
  expect(getByTestId('agents-row-clean').textContent).toContain('wash · main');
  expect(getByTestId('agents-row-dirty').textContent).toContain('wash · main*');
  // No branch (not a checkout) still shows where it is.
  expect(getByTestId('agents-row-nogit').textContent).toContain('tmp');
  expect(getByTestId('agents-row-nogit').textContent).not.toContain('·');
});

test('elapsed counts from the row anchor, not the push', () => {
  const rows = [row({ key: 'a' })];
  const { getByTestId } = render(() => (
    <AgentsWidget rows={() => rows} startedAt={() => 1_000} now={() => 91_000} onFocus={noop} />
  ));
  expect(getByTestId('agents-row-a').textContent).toContain('1m');
});

test('clicking a row asks to focus that agent’s terminal', () => {
  const rows = [row({ key: 'a' }), row({ key: 'b', term_instance: 'i-2' })];
  const seen: string[] = [];
  const { getByTestId } = render(() => (
    <AgentsWidget rows={() => rows} startedAt={at} now={() => 0} onFocus={(r) => seen.push(r.term_instance)} />
  ));
  fireEvent.click(getByTestId('agents-row-b'));
  expect(seen).toEqual(['i-2']);
});

test('state language matches the terminal’s own tab dot', () => {
  // Distinct colours for the three states a user acts on, and stale is
  // visibly not one of them.
  const colors = ['working', 'needs-input', 'done'].map(stateColor);
  expect(new Set(colors).size).toBe(3);
  expect(stateColor('stale')).not.toBe(stateColor('working'));
  // An unknown state still renders (a newer terminal can invent one).
  expect(stateColor('teleporting')).toBeTruthy();

  expect(stateLabel(row({ state: 'needs-input' }))).toBe('needs input');
  expect(stateLabel(row({ state: 'needs-input', reason: 'idle' }))).toBe('needs input · idle');
  expect(stateLabel(row({ state: 'stale' }))).toBe('not responding');
  expect(stateLabel(row({ state: 'working' }))).toBe('working');
});

test('fmtElapsed reads like a status line', () => {
  expect(fmtElapsed(0)).toBe('0s');
  expect(fmtElapsed(43_000)).toBe('43s');
  expect(fmtElapsed(90_000)).toBe('1m');
  expect(fmtElapsed(3 * 3_600_000)).toBe('3h');
  expect(fmtElapsed(-5_000)).toBe('0s');
});

// ---- M6: the question rows (docs/AGENT_TERM.md §12) ----

const ask = (over: Partial<AgentAsk> = {}): AgentAsk => ({
  id: 'ask-1',
  agent: 'claude',
  tool: 'Bash',
  subject: 'git push origin main',
  dir: 'wash',
  suggested_rule: 'Bash(git push*)',
  row_key: 'i-1:5',
  term_instance: 'i-1',
  age_ms: 0,
  ...over,
});

test('a pending question renders what the agent wants and three ways out', () => {
  const { getByTestId } = render(() => (
    <AgentsWidget rows={() => []} startedAt={at} now={() => 0} onFocus={noop} asks={() => [ask()]} />
  ));
  const row = getByTestId('agents-ask');
  expect(row.getAttribute('data-tool')).toBe('Bash');
  expect(row.getAttribute('data-ask-id')).toBe('ask-1');
  expect(getByTestId('agents-ask-what').textContent).toContain('git push origin main');
  // The rule "always" would write is named ON the button — what you
  // clicked is what gets saved.
  expect(getByTestId('agents-ask-always').textContent).toContain('Bash(git push*)');
  expect(getByTestId('agents-ask-allow')).toBeTruthy();
  expect(getByTestId('agents-ask-deny')).toBeTruthy();
});

test('each button answers with its own decision + remember flag', () => {
  const seen: string[] = [];
  const onAnswer = (_a: AgentAsk, d: string, r: boolean) => seen.push(`${d}:${r}`);
  const { getByTestId, unmount } = render(() => (
    <AgentsWidget rows={() => []} startedAt={at} now={() => 0} onFocus={noop} asks={() => [ask()]} onAnswer={onAnswer} />
  ));
  fireEvent.click(getByTestId('agents-ask-allow'));
  fireEvent.click(getByTestId('agents-ask-always'));
  fireEvent.click(getByTestId('agents-ask-deny'));
  expect(seen).toEqual(['allow:false', 'allow:true', 'deny:false']);
  unmount();
});

test('a question with no suggestion still offers allow and deny', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <AgentsWidget rows={() => []} startedAt={at} now={() => 0} onFocus={noop}
      asks={() => [ask({ suggested_rule: undefined })]} />
  ));
  expect(queryByTestId('agents-ask-always')).toBeNull();
  expect(getByTestId('agents-ask-allow')).toBeTruthy();
});

test('questions render above the status rows — blocked beats informational', () => {
  const { container } = render(() => (
    <AgentsWidget rows={() => [row({ key: 'r1' })]} startedAt={at} now={() => 0} onFocus={noop} asks={() => [ask()]} />
  ));
  const ids = [...container.querySelectorAll('[data-testid]')].map((el) => el.getAttribute('data-testid'));
  expect(ids.indexOf('agents-ask')).toBeLessThan(ids.indexOf('agents-row-r1'));
});

// ---- M7: the Recent list (docs/AGENT_TERM.md §13) ----

const sess = (over: Partial<AgentSession> = {}): AgentSession => ({
  session_id: 'sess-1',
  agent: 'claude',
  cwd: '/home/mick/wash',
  dir: 'wash',
  last_seen: Math.floor(Date.now() / 1000) - 300,
  ...over,
});

test('fmtAgo reads like a human said it', () => {
  const now = 1_000_000_000_000; // ms
  const sec = now / 1000;
  expect(fmtAgo(now, sec - 5)).toBe('just now');
  expect(fmtAgo(now, sec - 300)).toBe('5m ago');
  expect(fmtAgo(now, sec - 7200)).toBe('2h ago');
  expect(fmtAgo(now, sec - 2 * 86_400)).toBe('2d ago');
  expect(fmtAgo(now, 0)).toBe('');
});

// Earlier sessions moved to the Agent app's History menu: the sidebar
// answers "what is running", and a list of things that are NOT running
// was answering a different question in the same space.
test('the sidebar no longer lists sessions that ended', () => {
  const { queryByTestId } = render(() => (
    <AgentsWidget rows={() => []} startedAt={at} now={() => Date.now()} onFocus={noop}
      recent={() => [sess()]} />
  ));
  expect(queryByTestId('agents-recent')).toBeNull();
  expect(queryByTestId('agents-recent-row')).toBeNull();
});
