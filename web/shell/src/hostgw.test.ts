// Tests for the shell-side host-awareness map (docs/SIDEBAR.md M1).
//
// Pure module state — no Conn, no DOM — so this runs framework-free.
// Run with: node --test --conditions=browser web/shell/src/hostgw.test.ts

import { test, beforeEach } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  HOSTGW_STATE_KIND,
  dropHostgwOrigin,
  hostgwState,
  ingestHostgwMsg,
  onHostgwState,
  resetHostgw,
} from './hostgw.ts';

const push = (origin: string, service: string, state: unknown) =>
  ingestHostgwMsg(origin, { kind: HOSTGW_STATE_KIND, service, state });

beforeEach(() => resetHostgw());

test('a push lands under its origin and service', () => {
  assert.equal(push('build01', 'notify', { notifications: [1] }), true);
  assert.deepEqual(hostgwState().get('build01')?.get('notify'), { notifications: [1] });
});

test('origins are isolated — B\'s state never shows as A\'s', () => {
  // The regression that motivated the whole plan: the rail showed A's
  // agents while a B agent ran. Same cell, two hosts, no bleed.
  push('local', 'agent', { rows: ['a-local'] });
  push('build01', 'agent', { rows: ['a-remote'] });
  assert.deepEqual(hostgwState().get('local')?.get('agent'), { rows: ['a-local'] });
  assert.deepEqual(hostgwState().get('build01')?.get('agent'), { rows: ['a-remote'] });
});

test('a snapshot REPLACES its cell rather than merging into it', () => {
  // §3.2(4): badges recompute from snapshots. A merge would leave a
  // cleared queue looking non-empty forever.
  push('build01', 'priv', { queue: ['r1', 'r2'] });
  push('build01', 'priv', { queue: [] });
  assert.deepEqual(hostgwState().get('build01')?.get('priv'), { queue: [] });
});

test('services within one host coexist', () => {
  push('build01', 'priv', { queue: [] });
  push('build01', 'bulk', { jobs: ['j1'] });
  assert.equal(hostgwState().get('build01')?.size, 2);
});

test('a non-hostgw payload is refused, not stored', () => {
  assert.equal(ingestHostgwMsg('build01', { kind: 'notify.state', state: {} }), false);
  assert.equal(ingestHostgwMsg('build01', null), false);
  assert.equal(hostgwState().size, 0);
});

test('a malformed service name is refused', () => {
  // hostgw is the attested sender, but a bad frame from it must not put
  // an undefined key in the map and break every reader downstream.
  assert.equal(ingestHostgwMsg('build01', { kind: HOSTGW_STATE_KIND, state: {} }), false);
  assert.equal(ingestHostgwMsg('build01', { kind: HOSTGW_STATE_KIND, service: '', state: {} }), false);
  assert.equal(hostgwState().size, 0);
});

test('dropping an origin forgets that host only', () => {
  push('local', 'notify', { notifications: [] });
  push('build01', 'notify', { notifications: [1] });
  dropHostgwOrigin('build01');
  assert.equal(hostgwState().has('build01'), false);
  assert.equal(hostgwState().has('local'), true);
});

test('dropping an unknown origin is a no-op, not a notification', () => {
  let fires = 0;
  const off = onHostgwState(() => { fires++; });
  assert.equal(fires, 1); // on() fires immediately with the current value
  dropHostgwOrigin('never-attached');
  assert.equal(fires, 1);
  off();
});

test('subscribers see a fresh map identity per change, so downstream signals re-run', () => {
  const seen: unknown[] = [];
  const off = onHostgwState((m) => seen.push(m));
  push('build01', 'notify', { notifications: [] });
  push('build01', 'notify', { notifications: [1] });
  assert.equal(seen.length, 3); // initial + two pushes
  assert.notEqual(seen[1], seen[2]);
  off();
});
