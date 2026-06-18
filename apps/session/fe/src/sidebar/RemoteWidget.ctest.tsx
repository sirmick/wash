// Component test (Tier B) for the remote-sessions sidebar widget — a pure
// renderer of the com.wash.remote supervisor's host list. Covers the empty
// state, host rows (by origin) with status, the per-host launch dropdown,
// and the Manage callback. The supervisor SSH round-trip + real catalog
// delivery stay in higher tiers (manual two-VM verification).

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { RemoteWidget, type RemoteHost, type RemoteApp } from './RemoteWidget.tsx';

afterEach(cleanup);

const noApps = (_origin: string): RemoteApp[] => [];

test('empty: shows the no-sessions hint and the Manage button', () => {
  const { queryByTestId } = render(() => (
    <RemoteWidget hosts={() => []} appsFor={noApps} onLaunch={() => {}} onManage={() => {}} />
  ));
  expect(queryByTestId('remote-empty')).toBeTruthy();
  expect(queryByTestId('remote-hosts')).toBeNull();
  expect(queryByTestId('remote-manage')).toBeTruthy();
});

test('hosts render one row each (by origin) carrying their status', () => {
  const hosts: RemoteHost[] = [
    { host: 'alice@build01', origin: 'alice@build01', status: 'up' },
    { host: 'root@db', origin: 'root@db', status: 'reconnecting' },
  ];
  const { getByTestId, queryByTestId } = render(() => (
    <RemoteWidget hosts={() => hosts} appsFor={noApps} onLaunch={() => {}} onManage={() => {}} />
  ));
  expect(queryByTestId('remote-empty')).toBeNull();
  expect(getByTestId('remote-hosts')).toBeTruthy();
  expect(getByTestId('remote-host-alice@build01').getAttribute('data-status')).toBe('up');
  expect(getByTestId('remote-host-root@db').getAttribute('data-status')).toBe('reconnecting');
});

test('clicking a connected host opens its launch dropdown; picking an app fires onLaunch', () => {
  const hosts: RemoteHost[] = [{ host: 'h', origin: 'h', status: 'up' }];
  const apps: RemoteApp[] = [{ id: 'com.wash.term', name: 'Terminal' }];
  let launched: [string, string] | null = null;
  const { getByTestId, queryByTestId } = render(() => (
    <RemoteWidget
      hosts={() => hosts}
      appsFor={() => apps}
      onLaunch={(origin, id) => { launched = [origin, id]; }}
      onManage={() => {}}
    />
  ));
  // Dropdown is closed until the host is clicked.
  expect(queryByTestId('remote-apps-h')).toBeNull();
  fireEvent.click(getByTestId('remote-host-h'));
  expect(getByTestId('remote-apps-h')).toBeTruthy();
  fireEvent.click(getByTestId('remote-launch-h-com.wash.term'));
  expect(launched).toEqual(['h', 'com.wash.term']);
  // Picking an app closes the dropdown.
  expect(queryByTestId('remote-apps-h')).toBeNull();
});

test('clicking a non-connected host, or Manage, fires onManage', () => {
  let n = 0;
  const hosts: RemoteHost[] = [{ host: 'h', origin: 'h', status: 'down' }];
  const { getByTestId } = render(() => (
    <RemoteWidget hosts={() => hosts} appsFor={noApps} onLaunch={() => {}} onManage={() => { n++; }} />
  ));
  fireEvent.click(getByTestId('remote-host-h')); // down → Manage
  fireEvent.click(getByTestId('remote-manage'));
  expect(n).toBe(2);
});
