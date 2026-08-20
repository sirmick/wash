// Tests for the rail's awareness maths (docs/SIDEBAR.md M1b). Pure
// functions over the merged hostgw map — no DOM, no Solid.
//
// Run with: node --test --conditions=browser apps/session/fe/src/sidebar/awareness.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  LOCAL_ORIGIN,
  SERVICE_AGENT,
  SERVICE_BULK,
  SERVICE_NOTIFY,
  SERVICE_PRIV,
  activeBulkJobs,
  badgeText,
  countByHost,
  netBadge,
  netHostsNeedingAttention,
  orderOrigins,
  pendingPrivReqs,
  stateFor,
  totalCount,
  unreadNotifications,
  waitingAgents,
  type HostgwMap,
} from './awareness.ts';

/** mk builds a map from a plain {origin: {service: state}} literal. */
const mk = (spec: Record<string, Record<string, unknown>>): HostgwMap => {
  const outer = new Map<string, Map<string, unknown>>();
  for (const [origin, services] of Object.entries(spec)) {
    outer.set(origin, new Map(Object.entries(services)));
  }
  return outer;
};

test('origins read local-first, remotes alphabetical', () => {
  const m = mk({ zeta: {}, build01: {}, local: {}, alpha: {} });
  assert.deepEqual(orderOrigins(m), [LOCAL_ORIGIN, 'alpha', 'build01', 'zeta']);
});

test('a seat with no local router still orders its remotes', () => {
  assert.deepEqual(orderOrigins(mk({ b: {}, a: {} })), ['a', 'b']);
});

test('badges sum across hosts — the whole point of merging', () => {
  const m = mk({
    local: { notify: { notifications: [{ read: false }, { read: true }] } },
    build01: { notify: { notifications: [{ read: false }, { read: false }] } },
  });
  assert.equal(totalCount(m, SERVICE_NOTIFY, unreadNotifications), 3);
});

test('each host is counted ONCE, even though local state arrives twice', () => {
  // The double-counting trap M1b exists to avoid: local state now reaches
  // the FE via hostgw AND via the legacy notify.state kind. The badge has
  // exactly one source — this map — so the second delivery cannot inflate
  // it. A regression here would read 4, not 2.
  const m = mk({ local: { notify: { notifications: [{ read: false }, { read: false }] } } });
  assert.equal(totalCount(m, SERVICE_NOTIFY, unreadNotifications), 2);
});

test('per-host breakdown keeps only the hosts with something to say', () => {
  const m = mk({
    local: { priv: { queue: [] } },
    build01: { priv: { queue: [{ status: 'queued' }] } },
    idle02: { priv: { queue: [{ status: 'approved' }] } },
  });
  assert.deepEqual(countByHost(m, SERVICE_PRIV, pendingPrivReqs), [
    { origin: 'build01', count: 1 },
  ]);
});

test('a service no host reports is simply absent, not zero-crashing', () => {
  const m = mk({ local: { notify: { notifications: [] } } });
  assert.deepEqual(countByHost(m, SERVICE_BULK, activeBulkJobs), []);
  assert.equal(totalCount(m, SERVICE_BULK, activeBulkJobs), 0);
  assert.equal(stateFor(m, 'nohost', SERVICE_BULK), undefined);
});

test('malformed or empty state counts as zero rather than throwing', () => {
  assert.equal(unreadNotifications(null), 0);
  assert.equal(unreadNotifications({}), 0);
  assert.equal(activeBulkJobs(undefined), 0);
  assert.equal(pendingPrivReqs({ queue: undefined }), 0);
  assert.equal(waitingAgents({}), 0);
});

test('bulk counts in-flight work only — terminal rows are informational', () => {
  const jobs = [
    { status: 'queued' }, { status: 'running' },
    { status: 'done' }, { status: 'failed' }, { status: 'cancelled' },
  ];
  assert.equal(activeBulkJobs({ jobs }), 2);
});

test('priv counts what is still waiting on a human', () => {
  const queue = [{ status: 'queued' }, { status: 'queued' }, { status: 'approved' }, { status: 'rejected' }];
  assert.equal(pendingPrivReqs({ queue }), 2);
});

test('agents: a pending question counts the same as a blocked agent', () => {
  const state = {
    rows: [{ state: 'needs-input' }, { state: 'working' }],
    asks: [{ id: 'q1' }],
  };
  assert.equal(waitingAgents(state), 2);
});

test('agent badge merges hosts: one blocked here, one blocked there', () => {
  const m = mk({
    local: { agent: { rows: [{ state: 'working' }], asks: [] } },
    build01: { agent: { rows: [{ state: 'needs-input' }], asks: [] } },
    build02: { agent: { rows: [], asks: [{ id: 'q' }] } },
  });
  assert.equal(badgeText(totalCount(m, SERVICE_AGENT, waitingAgents)), '2');
});

test('badge text hides at zero', () => {
  assert.equal(badgeText(0), '');
  assert.equal(badgeText(3), '3');
});

test('net: an await-confirm anywhere outranks every other host', () => {
  // Someone is one timeout away from locking themselves out of a machine.
  // That must win over another host merely having failed earlier.
  const m = mk({
    local: { net: { status: 'failed' } },
    build01: { net: { status: 'await-confirm' } },
  });
  assert.equal(netBadge(m), '!');
});

test('net: with nothing urgent, the first terminal outcome shows', () => {
  const m = mk({
    local: { net: { status: 'committed' } },
    build01: { net: { status: 'reverted' } },
  });
  assert.equal(netBadge(m), 'reverted');
});

test('net: a settled fleet shows nothing', () => {
  assert.equal(netBadge(mk({ local: { net: { status: 'committed' } } })), '');
  assert.equal(netBadge(mk({})), '');
});

test('net: the per-host list does not collapse the way the badge does', () => {
  const m = mk({
    local: { net: { status: 'failed' } },
    build01: { net: { status: 'await-confirm' } },
    build02: { net: { status: 'committed' } },
  });
  assert.deepEqual(netHostsNeedingAttention(m), [
    { origin: LOCAL_ORIGIN, badge: 'failed' },
    { origin: 'build01', badge: '!' },
  ]);
});
