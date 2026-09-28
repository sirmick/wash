// Component test (Tier B) for the workspace sidebar's Team tree.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import type { agentproto } from '@wash/ui';
import { WorkspaceSidebar } from './WorkspaceSidebar.tsx';

afterEach(cleanup);

const frame = (members: Partial<agentproto.Member>[]) => ({
  workspace: { id: 'w', name: 'W', state: 'active', plan: [{ id: 'K1', title: 'Timers', state: 'active' }], qa: [], messages: [], members },
}) as unknown as agentproto.WorkspaceState;

const sidebar = (members: Partial<agentproto.Member>[]) =>
  render(() => <WorkspaceSidebar frame={frame(members)} selected="" onSelect={() => {}} onAction={() => {}} />);

// A long run ends dozens of members; they are hidden until asked for, and a
// node whose members have all ended leaves the tree with them.
test('ended members leave the Team tree until asked for', () => {
  const { getByTestId, queryByTestId } = sidebar([
    { id: 'lead', name: 'Orchestrator', state: 'available' },
    { id: 'old', name: 'Old implementer', state: 'ended', node: 'K1' },
    { id: 'gone', name: 'Gone reviewer', state: 'ended' },
  ]);
  expect(queryByTestId('workspace-member-lead')).not.toBeNull();
  expect(queryByTestId('workspace-member-old')).toBeNull();
  expect(queryByTestId('workspace-member-gone')).toBeNull();
  expect(queryByTestId('workspace-team-K1')).toBeNull();

  const toggle = getByTestId('workspace-show-ended') as HTMLInputElement;
  expect(toggle.closest('label')?.textContent).toContain('Show ended (2)');
  fireEvent.click(toggle);
  expect(queryByTestId('workspace-member-old')).not.toBeNull();
  expect(queryByTestId('workspace-member-gone')).not.toBeNull();
  expect(queryByTestId('workspace-team-K1')).not.toBeNull();
});

test('with nobody ended there is no toggle', () => {
  const { queryByTestId } = sidebar([{ id: 'lead', name: 'Orchestrator', state: 'available' }]);
  expect(queryByTestId('workspace-show-ended')).toBeNull();
});
