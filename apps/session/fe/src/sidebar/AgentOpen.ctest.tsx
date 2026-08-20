// Component test (Tier B) for the Agents section's way in
// (docs/SIDEBAR.md M2c). The roster moved to com.wash.ai; what stays is
// "what needs me, and where", plus a door per host.
//
// This is the deep-link the §3.2(7) tripwire watches, so what's asserted
// is what would make it feel like a toll: does the button say what you'd
// find on the other side, and does it open the RIGHT host.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { AgentOpen } from './AgentOpen.tsx';
import type { AgentHostSummary } from './awareness.ts';

afterEach(cleanup);

const host = (over: Partial<AgentHostSummary> = {}): AgentHostSummary => ({
  origin: 'build01', waiting: 0, running: 0, ...over,
});

test('local always has a door, even with nothing running', () => {
  // Opening the app on this machine is how a NEW session starts, so the
  // button can't be conditional on there already being one.
  const { getByTestId } = render(() => <AgentOpen hosts={() => []} onOpen={() => {}} />);
  expect(getByTestId('agents-open-local').textContent).toContain('Open Agent');
});

test('with agents running locally it says how many', () => {
  const { getByTestId } = render(() => (
    <AgentOpen hosts={() => [host({ origin: 'local', running: 2 })]} onOpen={() => {}} />
  ));
  expect(getByTestId('agents-open-local').textContent).toContain('2 running');
});

test('someone waiting outranks someone working', () => {
  // "3 running" is reassurance; "1 waiting" is a claim on your attention.
  const { getByTestId } = render(() => (
    <AgentOpen hosts={() => [host({ origin: 'local', waiting: 1, running: 3 })]} onOpen={() => {}} />
  ));
  expect(getByTestId('agents-open-local').textContent).toContain('1 waiting');
  expect(getByTestId('agents-open-local').textContent).not.toContain('running');
});

test('a busy remote host gets a door even with nobody blocked', () => {
  // The first cut keyed the door off waiting alone, so a host with an
  // agent working away had no way in — caught by the two-router e2e.
  // Watching or stopping a working agent is a reason to open the app.
  const { getByTestId } = render(() => (
    <AgentOpen hosts={() => [host({ origin: 'build01', running: 1 })]} onOpen={() => {}} />
  ));
  expect(getByTestId('agents-open-build01').textContent).toContain('1 running');
  expect(getByTestId('agents-open-build01').textContent).toContain('build01');
});

test('a remote host waiting on you is named and badged', () => {
  const { getByTestId } = render(() => (
    <AgentOpen hosts={() => [host({ origin: 'build01', waiting: 2, running: 3 })]} onOpen={() => {}} />
  ));
  const btn = getByTestId('agents-open-build01');
  expect(btn.textContent).toContain('2 waiting');
  expect(btn.textContent).toContain('build01');
  // data-badge since M3c, when the button became the shared <HostOpen>.
  expect(btn.getAttribute('data-badge')).toBe('2');
  expect(getByTestId('agents-open-badge-build01').textContent).toBe('2');
});

test('opening addresses the host the button belongs to', () => {
  // The whole point of the relocation: launchOn carries an origin, and
  // the verbs the rail used to hold could not.
  const opened: string[] = [];
  const { getByTestId } = render(() => (
    <AgentOpen hosts={() => [host({ waiting: 1 })]} onOpen={(o) => { opened.push(o); }} />
  ));
  fireEvent.click(getByTestId('agents-open-build01'));
  fireEvent.click(getByTestId('agents-open-local'));
  expect(opened).toEqual(['build01', 'local']);
});

test('local comes first, remotes after', () => {
  const { container } = render(() => (
    <AgentOpen
      hosts={() => [host({ origin: 'build01', waiting: 1 }), host({ origin: 'local', waiting: 2 })]}
      onOpen={() => {}}
    />
  ));
  const ids = [...container.querySelectorAll('[data-origin]')].map((e) => e.getAttribute('data-origin'));
  expect(ids).toEqual(['local', 'build01']);
});

test('a quiet remote host gets no door — the rail is not a host list', () => {
  // Hosts appear when they have something to say. Listing every attached
  // machine here would duplicate the Remote section.
  const { queryByTestId } = render(() => <AgentOpen hosts={() => []} onOpen={() => {}} />);
  expect(queryByTestId('agents-open-build01')).toBeNull();
});
