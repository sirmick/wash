// Component test (Tier B) for the priv sidebar widget — pure renderer of the
// privilege-request queue + lock state. Covers empty/populated, lock-bar
// state, the unlock button, and approve/reject on a pending request. The
// cross-process unlock→exec flow stays in priv.spec.ts.

import { test, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@solidjs/testing-library';
import { PrivWidget, type PrivReq } from './PrivWidget.tsx';

afterEach(cleanup);

const req = (over: Partial<PrivReq> = {}): PrivReq => ({
  req_id: 'q1',
  kind: 'run',
  sender_app_id: 'com.wash.test',
  sender_inst_id: 'i1',
  argv: ['apt-get', 'update'],
  reason: 'install',
  status: 'queued',
  ...over,
});

test('empty + unlocked: empty hint, lock-bar unlocked, unlock button present', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <PrivWidget locked={() => false} reqs={() => []} onApprove={() => {}} onReject={() => {}} onLock={() => {}} />
  ));
  expect(queryByTestId('priv-empty')).toBeTruthy();
  expect(getByTestId('priv-lock-bar').getAttribute('data-locked')).toBe('false');
  expect(queryByTestId('priv-lock')).toBeTruthy();
});

test('locked: data-locked=true and no unlock button', () => {
  const { getByTestId, queryByTestId } = render(() => (
    <PrivWidget locked={() => true} reqs={() => []} onApprove={() => {}} onReject={() => {}} onLock={() => {}} />
  ));
  expect(getByTestId('priv-widget').getAttribute('data-locked')).toBe('true');
  expect(queryByTestId('priv-lock')).toBeNull();
});

test('a queued request renders with approve/reject; clicks fire by id', () => {
  const approved: string[] = [];
  const rejected: string[] = [];
  const { getByTestId } = render(() => (
    <PrivWidget
      locked={() => false}
      reqs={() => [req({ req_id: 'a', status: 'queued' })]}
      onApprove={(id) => approved.push(id)}
      onReject={(id) => rejected.push(id)}
      onLock={() => {}}
    />
  ));
  expect(getByTestId('priv-req-a').getAttribute('data-status')).toBe('queued');
  fireEvent.click(getByTestId('priv-approve-a'));
  expect(approved).toEqual(['a']);
  fireEvent.click(getByTestId('priv-reject-a'));
  expect(rejected).toEqual(['a']);
});

test('clicking the lock bar unlock button fires onLock', () => {
  let locks = 0;
  const { getByTestId } = render(() => (
    <PrivWidget locked={() => false} reqs={() => []} onApprove={() => {}} onReject={() => {}} onLock={() => { locks++; }} />
  ));
  fireEvent.click(getByTestId('priv-lock'));
  expect(locks).toBe(1);
});
