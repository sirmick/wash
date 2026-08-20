// Component test (Tier B) for the rail's per-host awareness groups
// (docs/SIDEBAR.md M1c). Covers what a host group has to get right:
// which hosts appear at all, the collapsed-but-badged default, the
// reconnecting/down policy from §3.2(4), and the focus emphasis.
//
// Colour is asserted only as "this host resolves to a hue", never as a
// computed value: the accent tokens are `var(--wash-accent-*, #fallback)`
// and jsdom's CSS parser drops a var() colour, so a value assertion would
// be testing jsdom. Same reasoning as web/shell/src/notify.ctest.ts; the
// rendered hue belongs to the screenshot tier.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { HostGroups, groupID } from './HostGroups.tsx';
import { hostHue } from './host-hue.ts';
import type { HostGroupRow } from './awareness.ts';
import type { SectionState } from './Section.tsx';

afterEach(cleanup);

const row = (over: Partial<HostGroupRow> = {}): HostGroupRow => ({
  origin: 'build01',
  badge: '2',
  summary: '2 waiting on you',
  status: 'up',
  ...over,
});

// mount renders the groups with a mutable per-id collapse state, so a
// click can be observed the way the real section-state machinery behaves.
function mount(rows: HostGroupRow[], states: Record<string, SectionState> = {}, focused?: string) {
  const toggled: string[] = [];
  const view = render(() => (
    <HostGroups
      section="agents"
      rows={() => rows}
      stateFor={(id) => states[id] ?? 'collapsed'}
      onToggle={(id) => { toggled.push(id); }}
      hostColor={(o) => hostHue(o)}
      focusedOrigin={() => focused}
    />
  ));
  return { ...view, toggled };
}

test('no remote hosts: nothing rendered at all, not an empty box', () => {
  const { queryByTestId } = mount([]);
  expect(queryByTestId('host-groups-agents')).toBeNull();
});

test('one group per host, named and badged', () => {
  const { getByTestId } = mount([
    row({ origin: 'build01', badge: '2' }),
    row({ origin: 'build02', badge: '1' }),
  ]);
  expect(getByTestId('host-group-name-agents-build01').textContent).toBe('build01');
  expect(getByTestId('host-group-badge-agents-build01').textContent).toBe('2');
  expect(getByTestId('host-group-badge-agents-build02').textContent).toBe('1');
});

test('collapsed by default — badged, but the summary is folded away', () => {
  const { getByTestId, queryByTestId } = mount([row()]);
  expect(getByTestId('host-group-agents-build01').getAttribute('data-state')).toBe('collapsed');
  expect(queryByTestId('host-group-body-agents-build01')).toBeNull();
  // The badge is the whole point of collapsed-but-badged.
  expect(getByTestId('host-group-badge-agents-build01').textContent).toBe('2');
});

test('expanded shows the summary — what, not just that', () => {
  const states = { [groupID('agents', 'build01')]: 'expanded' as SectionState };
  const { getByTestId } = mount([row({ summary: '1 waiting on you · 2 agents working' })], states);
  expect(getByTestId('host-group-body-agents-build01').textContent)
    .toBe('1 waiting on you · 2 agents working');
});

test('a header click toggles through the section-state machinery', () => {
  // The id is what matters: it must be the "<section>:<origin>" key the
  // rail persists and auto-expands, not a private toggle.
  const { getByTestId, toggled } = mount([row()]);
  fireEvent.click(getByTestId('host-group-header-agents-build01'));
  expect(toggled).toEqual(['agents:build01']);
});

test('a reconnecting host is labelled and dimmed, not hidden', () => {
  // §3.2(4): a greyed stale count is honest; a confident stale count lies.
  const { getByTestId } = mount([row({ status: 'reconnecting' })]);
  const group = getByTestId('host-group-agents-build01');
  expect(group.getAttribute('data-status')).toBe('reconnecting');
  expect(getByTestId('host-group-stale-agents-build01').textContent).toBe('reconnecting');
  // Still counted — the number is stale, not wrong.
  expect(getByTestId('host-group-badge-agents-build01').textContent).toBe('2');
  expect(Number(group.style.opacity)).toBeLessThan(1);
});

test('an up host carries no staleness marker', () => {
  const { queryByTestId, getByTestId } = mount([row({ status: 'up' })]);
  expect(queryByTestId('host-group-stale-agents-build01')).toBeNull();
  expect(Number(getByTestId('host-group-agents-build01').style.opacity)).toBe(1);
});

test('the focused window\'s host is marked, so "where you are" reads', () => {
  const { getByTestId } = mount(
    [row({ origin: 'build01' }), row({ origin: 'build02' })],
    {},
    'build02',
  );
  expect(getByTestId('host-group-agents-build02').getAttribute('data-focused')).toBe('true');
  expect(getByTestId('host-group-agents-build01').getAttribute('data-focused')).toBe('false');
});

test('a host with a badge but no summary still says something when opened', () => {
  const states = { [groupID('agents', 'build01')]: 'expanded' as SectionState };
  const { getByTestId } = mount([row({ summary: '' })], states);
  expect(getByTestId('host-group-body-agents-build01').textContent).toBe('nothing to report');
});

test('each host resolves to a hue, and keeps it', () => {
  // Behaviour, not the computed colour (see the file header).
  expect(hostHue('build01')).toBeTruthy();
  expect(hostHue('build01')).toBe(hostHue('build01'));
  const { getByTestId } = mount([row()]);
  expect(getByTestId('host-group-name-agents-build01')).toBeTruthy();
});

test('groups are keyed per section, so two sections do not collide', () => {
  expect(groupID('notify', 'build01')).toBe('notify:build01');
  expect(groupID('agents', 'build01')).toBe('agents:build01');
});
