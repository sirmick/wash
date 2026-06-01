// Component test (Tier B) for the net sidebar widget — pure renderer of the
// netd status snapshot + live interface list. Covers the null/idle state,
// the no-address fallback vs interface rows, and the Configure callback. The
// cross-process netd round-trip stays in net-vm-gate / net-app specs.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { NetWidget, type NetState, type NetIface } from './NetWidget.tsx';

afterEach(cleanup);

test('null state: status falls back to idle, no-address shown, Configure present', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <NetWidget state={() => null} ifaces={() => []} onConfigure={() => {}} />
  ));
  // net-status is the colour dot; the status string is on net-widget's
  // data-status (falls back to "idle" when state is null).
  expect(getByTestId('net-widget').getAttribute('data-status')).toBe('idle');
  expect(getByTestId('net-status')).toBeTruthy();
  expect(queryByTestId('net-noaddr')).toBeTruthy();
  expect(queryByTestId('net-ifaces')).toBeNull();
  expect(queryByTestId('net-configure')).toBeTruthy();
});

test('interfaces render one row each (by name)', () => {
  const ifaces: NetIface[] = [
    { name: 'eth0', ips: ['192.168.1.2/24'] },
    { name: 'wg0', ips: ['10.0.0.1/32'] },
  ];
  const { getByTestId, queryByTestId } = render(() => (
    <NetWidget state={() => ({ status: 'committed' }) as NetState} ifaces={() => ifaces} onConfigure={() => {}} />
  ));
  expect(queryByTestId('net-noaddr')).toBeNull();
  expect(getByTestId('net-ifaces')).toBeTruthy();
  expect(getByTestId('net-iface-eth0')).toBeTruthy();
  expect(getByTestId('net-iface-wg0')).toBeTruthy();
});

test('await-confirm with a summary shows the pending banner', () => {
  const { queryByTestId } = render(() => (
    <NetWidget
      state={() => ({ status: 'await-confirm', summary: ['+ eth0 static 192.168.1.2/24'] }) as NetState}
      ifaces={() => []}
      onConfigure={() => {}}
    />
  ));
  expect(queryByTestId('net-pending')).toBeTruthy();
});

test('clicking Configure fires onConfigure', () => {
  let n = 0;
  const { getByTestId } = render(() => (
    <NetWidget state={() => null} ifaces={() => []} onConfigure={() => { n++; }} />
  ));
  fireEvent.click(getByTestId('net-configure'));
  expect(n).toBe(1);
});
