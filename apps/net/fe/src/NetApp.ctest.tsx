// Component test (Tier B): mount the whole NetApp shell. Its job here is a SMOKE
// GUARD. NetApp's setup creates several createMemos, which Solid runs EAGERLY at
// mount — so a memo that references a `const`/helper declared later in the
// component body throws a temporal-dead-zone ReferenceError on mount. That
// shipped once (an eager `looseConnections` memo called `routerCaps()` before its
// `const` declaration → "can't access lexical declaration … before
// initialization", a blank net window). A plain build + the *-model unit tests
// never mount NetApp, so only mounting it catches this class of bug.
import { test, expect, afterEach, beforeEach, vi } from 'vitest';
import { render, cleanup, waitFor, fireEvent } from '@solidjs/testing-library';
import { NetApp } from './main.tsx';

afterEach(cleanup);

beforeEach(() => {
  // The host bridge NetApp talks to: loadCurrent posts a 'current' request on
  // mount. A no-op stub suffices — the TDZ would throw during setup, before any
  // reply, and we only assert the component mounts + renders its root.
  (window as any).wash = { sendAppMsg: vi.fn() };
});

test('NetApp mounts without throwing (guards setup-time TDZ / declaration order)', () => {
  const host = document.createElement('div');
  expect(() => render(() => <NetApp instance="i-test" host={host} />)).not.toThrow();
});

test('NetApp renders its app root + the connections header', () => {
  const host = document.createElement('div');
  const { container } = render(() => <NetApp instance="i-test" host={host} />);
  expect(container.querySelector('.wash-net-app')).not.toBeNull();
  // The add bar always renders (workstation buttons); router buttons are caps-gated.
  expect(container.querySelector('[data-testid="add-ethernet"]')).not.toBeNull();
});

// Guards the router-caps key match: routerCaps() checks model.Kinds() keys, which
// are package/section ("firewall/zone", "dhcp/dhcp") — NOT bare names. A mismatch
// hides the whole router plane (this shipped: "available in router mode" with no
// Networks UI). Deliver a `current` reply with router caps and assert it reveals.
test('router caps reveal the Networks UI (+ Network button)', async () => {
  const sent: any[] = [];
  (window as any).wash = { sendAppMsg: (_inst: string, msg: any) => sent.push(msg) };
  const host = document.createElement('div');
  const { queryByTestId } = render(() => <NetApp instance="i-test" host={host} />);

  await waitFor(() => expect(sent.find((m) => m.kind === 'current')).toBeTruthy());
  const id = sent.find((m) => m.kind === 'current').id;
  host.dispatchEvent(new CustomEvent('wash:msg', {
    detail: {
      id, kind: 'current_ok', config: { Interfaces: [], Devices: [] }, devices: [],
      caps: { features: [], kinds: ['firewall/zone', 'dhcp/dhcp'] },
    },
  }));

  await waitFor(() => expect(queryByTestId('add-network')).not.toBeNull());
});

// Guards diagnostics surfacing: when the draft is dirty the FE live-validates and
// must render netd's diagnostics in the banner. This was the gap — applyState
// dropped r.diagnostics, so validation errors showed nothing (the reported bug).
test('validation diagnostics surface in the banner', async () => {
  const sent: any[] = [];
  (window as any).wash = { sendAppMsg: (_inst: string, msg: any) => sent.push(msg) };
  const host = document.createElement('div');
  const { queryByTestId, getByTestId } = render(() => <NetApp instance="i-test" host={host} />);

  await waitFor(() => expect(sent.find((m) => m.kind === 'current')).toBeTruthy());
  const curId = sent.find((m) => m.kind === 'current').id;
  host.dispatchEvent(new CustomEvent('wash:msg', { detail: {
    id: curId, kind: 'current_ok', config: { Interfaces: [], Devices: [] }, devices: ['eth9'], caps: { features: [], kinds: [] },
  }}));

  // Stage a connection → draft dirty → the debounced live-validate fires.
  await waitFor(() => expect(getByTestId('add-ethernet')).toBeTruthy());
  fireEvent.click(getByTestId('add-ethernet'));
  await waitFor(() => expect(getByTestId('eth-create')).toBeTruthy());
  fireEvent.click(getByTestId('eth-create'));

  // Answer the validate round-trip with a diagnostic; the banner must show it.
  await waitFor(() => expect(sent.find((m) => m.kind === 'validate')).toBeTruthy(), { timeout: 2000 });
  const vId = sent.find((m) => m.kind === 'validate').id;
  host.dispatchEvent(new CustomEvent('wash:msg', { detail: {
    id: vId, kind: 'validate_ok',
    diagnostics: [{ path: 'Interfaces[0].Proto.IPAddr', code: 'required', message: 'static interface needs an ipaddr', severity: 'error' }],
  }}));

  const banner = await waitFor(() => {
    const b = queryByTestId('net-diags');
    if (!b) throw new Error('no diagnostics banner');
    return b;
  });
  expect(banner.textContent).toContain('needs an ipaddr');
});
