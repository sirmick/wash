// Component test (Tier B) for the shared per-host door (docs/SIDEBAR.md
// M2c/M3c). AgentOpen.ctest covers the agent-specific labelling on top of
// this; what's asserted here is the contract every relocation relies on —
// the button opens the host it belongs to, and says what's behind it.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { HostOpen, type HostDoor } from './HostOpen.tsx';

afterEach(cleanup);

const bulk = (origin: string, count: number): HostDoor => ({
  origin,
  label: origin === 'local' ? `${count} in flight` : `${count} in flight · ${origin}`,
});

test('a door opens the host it belongs to, not the focused one', () => {
  // The entire point of the relocation: launchOn carries an origin, and
  // the verbs the rail used to hold could not.
  const opened: string[] = [];
  const { getByTestId } = render(() => (
    <HostOpen
      testid="bulk-open"
      what="file manager"
      doors={() => [bulk('local', 1), bulk('build01', 3)]}
      onOpen={(o) => { opened.push(o); }}
    />
  ));
  fireEvent.click(getByTestId('bulk-open-build01'));
  fireEvent.click(getByTestId('bulk-open-local'));
  expect(opened).toEqual(['build01', 'local']);
});

test('a remote door names its host; the local one does not', () => {
  // "3 in flight" with no host means this machine — the rail's local-first
  // convention, so the common case stays short.
  const { getByTestId } = render(() => (
    <HostOpen testid="bulk-open" what="file manager" doors={() => [bulk('local', 2), bulk('build01', 3)]} onOpen={() => {}} />
  ));
  expect(getByTestId('bulk-open-local').textContent).toContain('2 in flight');
  expect(getByTestId('bulk-open-local').textContent).not.toContain('build01');
  expect(getByTestId('bulk-open-build01').textContent).toContain('build01');
});

test('order is the caller\'s, so the rail stays local-first', () => {
  const { container } = render(() => (
    <HostOpen
      testid="bulk-open"
      what="file manager"
      doors={() => [bulk('local', 1), bulk('alpha', 1), bulk('build01', 1)]}
      onOpen={() => {}}
    />
  ));
  const ids = [...container.querySelectorAll('[data-origin]')].map((e) => e.getAttribute('data-origin'));
  expect(ids).toEqual(['local', 'alpha', 'build01']);
});

test('no badge pill unless something is actually waiting on a human', () => {
  // Bulk passes no badge at all: a copy in flight is progress, not a
  // claim on attention. Agents passes its waiting count.
  const { queryByTestId, getByTestId } = render(() => (
    <HostOpen
      testid="d"
      what="app"
      doors={() => [{ origin: 'local', label: 'busy' }, { origin: 'build01', label: 'blocked', badge: 2 }]}
      onOpen={() => {}}
    />
  ));
  expect(queryByTestId('d-badge-local')).toBeNull();
  expect(getByTestId('d-badge-build01').textContent).toBe('2');
  expect(getByTestId('d-local').getAttribute('data-badge')).toBe('0');
});

test('nothing to report means no doors — the rail is not a host list', () => {
  // Listing every attached machine here would duplicate the Remote section.
  const { container, getByTestId } = render(() => (
    <HostOpen testid="bulk-open" what="file manager" doors={() => []} onOpen={() => {}} />
  ));
  expect(getByTestId('bulk-open')).toBeTruthy();
  expect(container.querySelectorAll('[data-origin]')).toHaveLength(0);
});
