// Component test (Tier B): mount the whole NetApp shell. Its job here is a SMOKE
// GUARD. NetApp's setup creates several createMemos, which Solid runs EAGERLY at
// mount — so a memo that references a `const`/helper declared later in the
// component body throws a temporal-dead-zone ReferenceError on mount. That
// shipped once (an eager `looseConnections` memo called `routerCaps()` before its
// `const` declaration → "can't access lexical declaration … before
// initialization", a blank net window). A plain build + the *-model unit tests
// never mount NetApp, so only mounting it catches this class of bug.
import { test, expect, afterEach, beforeEach, vi } from 'vitest';
import { render, cleanup } from '@solidjs/testing-library';
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
