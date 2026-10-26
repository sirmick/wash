// Component test (Tier B) for the Permissions tab: edits are saved whole
// after typing settles, half-typed rows are never sent, a roster push never
// overwrites an edit in flight, and a change made elsewhere (a rule "always
// allow" just added) shows when nothing here is unsaved.

import { test, expect, afterEach, vi } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { createSignal } from 'solid-js';
import type { agentproto } from '@wash/ui';
import { Permissions, POLICY_SAVE_DELAY_MS } from './Permissions.tsx';

afterEach(() => {
  cleanup();
  vi.useRealTimers();
});

const base: agentproto.PolicyView = {
  enabled: true,
  default: 'ask',
  ask_desktop: true,
  rules: [{ match: 'Read', decision: 'allow' }],
};

const mount = (initial: agentproto.PolicyView | undefined) => {
  vi.useFakeTimers();
  const [policy, setPolicy] = createSignal(initial);
  const saved: agentproto.PolicyView[] = [];
  const r = render(() => <Permissions policy={policy()} onSave={(p) => saved.push(p)} />);
  return { ...r, saved, setPolicy };
};

const matches = (r: ReturnType<typeof mount>) =>
  r.getAllByTestId('agents-policy-rule-match').map((el) => (el as HTMLInputElement).value);

test('shows the policy, off and ask when there is none yet', () => {
  const r = mount(undefined);
  expect((r.getByTestId('agents-policy-enabled') as HTMLInputElement).checked).toBe(false);
  expect(r.getByTestId('agents-policy-rules-empty')).toBeTruthy();
  // "allow" is never offered as the default.
  const opts = [...(r.getByTestId('agents-policy-default') as HTMLSelectElement).options].map((o) => o.value);
  expect(opts).toEqual(['ask', 'deny']);
});

test('an edit is saved whole once typing settles; a row with no tool is not sent', () => {
  const r = mount(base);
  fireEvent.click(r.getByTestId('agents-policy-rule-add'));
  fireEvent.input(r.getAllByTestId('agents-policy-rule-match')[1], { target: { value: 'Bash(git' } });
  fireEvent.input(r.getAllByTestId('agents-policy-rule-match')[1], { target: { value: 'Bash(git status:*)' } });
  fireEvent.click(r.getByTestId('agents-policy-rule-add'));
  expect(r.saved).toEqual([]);
  vi.advanceTimersByTime(POLICY_SAVE_DELAY_MS);
  expect(r.saved).toHaveLength(1);
  expect(r.saved[0].rules).toEqual([
    { match: 'Read', decision: 'allow' },
    { match: 'Bash(git status:*)', decision: 'ask' },
  ]);
  // The empty row stays on screen for the user to fill in.
  expect(matches(r)).toEqual(['Read', 'Bash(git status:*)', '']);
});

test('a push during an edit does not overwrite it', () => {
  const r = mount(base);
  fireEvent.input(r.getAllByTestId('agents-policy-rule-match')[0], { target: { value: 'Grep' } });
  r.setPolicy({ ...base, rules: [...base.rules!, { match: 'WebFetch', decision: 'allow' }] });
  expect(matches(r)).toEqual(['Grep']);
  vi.advanceTimersByTime(POLICY_SAVE_DELAY_MS);
  expect(r.saved[0].rules).toEqual([{ match: 'Grep', decision: 'allow' }]);
});

test('a change made elsewhere shows when nothing here is unsaved', () => {
  const r = mount(base);
  // Own save echoed back: the draft stays (nothing to follow).
  fireEvent.click(r.getByTestId('agents-policy-enabled'));
  vi.advanceTimersByTime(POLICY_SAVE_DELAY_MS);
  r.setPolicy({ ...base, enabled: false });
  expect((r.getByTestId('agents-policy-enabled') as HTMLInputElement).checked).toBe(false);
  // Then "always allow" adds a rule: the tab follows the file.
  r.setPolicy({ ...base, enabled: false, rules: [...base.rules!, { match: 'Bash(ls:*)', decision: 'allow', cwd: '/src' }] });
  expect(matches(r)).toEqual(['Read', 'Bash(ls:*)']);
});

test('ask-me-here is greyed while the policy is off', () => {
  const r = mount({ ...base, enabled: false });
  expect((r.getByTestId('agents-policy-ask-desktop') as HTMLInputElement).disabled).toBe(true);
});

test('typing in a rule keeps the same input (focus is not lost per keystroke)', () => {
  const r = mount(base);
  const input = r.getAllByTestId('agents-policy-rule-match')[0];
  fireEvent.input(input, { target: { value: 'G' } });
  fireEvent.input(r.getAllByTestId('agents-policy-rule-match')[0], { target: { value: 'Gr' } });
  expect(r.getAllByTestId('agents-policy-rule-match')[0]).toBe(input);
});
