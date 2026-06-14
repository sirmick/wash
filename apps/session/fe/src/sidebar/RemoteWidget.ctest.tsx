// Component test (Tier B) for the remote-sessions sidebar widget — a pure
// renderer of the com.wash.remote supervisor's host list. Covers the empty
// state, host rows (by origin) with status, and the Manage callback. The
// supervisor SSH round-trip + wash-connect launch stay in higher tiers
// (manual two-VM verification).

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { RemoteWidget, type RemoteHost } from './RemoteWidget.tsx';

afterEach(cleanup);

test('empty: shows the no-sessions hint and the Manage button', () => {
  const { queryByTestId } = render(() => (
    <RemoteWidget hosts={() => []} onManage={() => {}} />
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
    <RemoteWidget hosts={() => hosts} onManage={() => {}} />
  ));
  expect(queryByTestId('remote-empty')).toBeNull();
  expect(getByTestId('remote-hosts')).toBeTruthy();
  expect(getByTestId('remote-host-alice@build01').getAttribute('data-status')).toBe('up');
  expect(getByTestId('remote-host-root@db').getAttribute('data-status')).toBe('reconnecting');
});

test('clicking a host or Manage fires onManage (opens wash-connect)', () => {
  let n = 0;
  const hosts: RemoteHost[] = [{ host: 'h', origin: 'h', status: 'up' }];
  const { getByTestId } = render(() => (
    <RemoteWidget hosts={() => hosts} onManage={() => { n++; }} />
  ));
  fireEvent.click(getByTestId('remote-host-h'));
  fireEvent.click(getByTestId('remote-manage'));
  expect(n).toBe(2);
});
