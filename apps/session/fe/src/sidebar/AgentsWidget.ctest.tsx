// Component test (Tier B) for the coding-agent roster widget — the pure
// renderer of com.wash.agentd's snapshot. Covers empty/populated
// rendering, the state language (colour + label), the "dir · branch*"
// place line, and the click-to-focus callback. The cross-process flow (a
// real agent in a real terminal reaching the sidebar) is
// term-agent-roster.spec.ts; that's the integration seam.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { AgentsWidget, fmtElapsed, stateColor, stateLabel, type AgentRow } from './AgentsWidget.tsx';

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
