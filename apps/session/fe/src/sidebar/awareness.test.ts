// Tests for the rail's awareness maths (docs/SIDEBAR.md M1b). Pure
// functions over the merged hostgw map — no DOM, no Solid.
//
// Run with: node --test --conditions=browser apps/session/fe/src/sidebar/awareness.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  AWARENESS_COUNTERS,
  LOCAL_ORIGIN,
  SERVICE_AGENT,
  SERVICE_BULK,
  SERVICE_NET,
  SERVICE_NOTIFY,
  SERVICE_PRIV,
  activeBulkJobs,
  agentHostSummary,
  agentSummary,
  badgeText,
  bulkSummary,
  countBadge,
  countByHost,
  hostsWithService,
  netBadge,
  netBadgeForHost,
  netHostsNeedingAttention,
  netSummary,
  netUrgency,
  notifySummary,
  orderOrigins,
  pendingPrivReqs,
  privSummary,
  remoteHostRows,
  runningAgents,
  sectionForService,
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

test('a door predicate counts HAVING a service, not having a problem', () => {
  // The distinction M5 turns on. netd's door is warranted by the host
  // existing — a network that is quietly fine is still a thing you open
  // settings for — whereas bulk's door is warranted by work in flight.
  // Keying net off countByHost would have hidden the door on exactly the
  // hosts whose network was working.
  const m = mk({
    local: { net: { status: 'committed' } },
    build01: { net: { status: 'await-confirm' } },
    idle02: { net: { status: 'committed' } },
  });
  assert.deepEqual(hostsWithService(m, SERVICE_NET), [LOCAL_ORIGIN, 'build01', 'idle02']);
  // ...while the badge maths still names only the host in trouble.
  assert.deepEqual(countByHost(m, SERVICE_NET, netUrgency), [{ origin: 'build01', count: 1 }]);
});

test('hosts with a service come back local-first, then sorted', () => {
  const m = mk({
    zeta: { net: { status: 'committed' } },
    alpha: { net: { status: 'committed' } },
    local: { net: { status: 'committed' } },
  });
  assert.deepEqual(hostsWithService(m, SERVICE_NET), [LOCAL_ORIGIN, 'alpha', 'zeta']);
});

test('a host without the service gets no door', () => {
  const m = mk({
    local: { net: { status: 'committed' } },
    build01: { bulk: { jobs: [] } },
  });
  assert.deepEqual(hostsWithService(m, SERVICE_NET), [LOCAL_ORIGIN]);
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

// ---- per-host rows + summaries (M1c) ----

const upEverywhere = () => 'up' as const;

test('summaries answer "what", and stay silent when there is nothing', () => {
  assert.equal(notifySummary({ notifications: [{ read: false }] }), '1 unread');
  assert.equal(notifySummary({ notifications: [{ read: true }] }), '');
  assert.equal(bulkSummary({ jobs: [{ status: 'running' }] }), '1 job in flight');
  assert.equal(bulkSummary({ jobs: [{ status: 'running' }, { status: 'queued' }] }), '2 jobs in flight');
  assert.equal(privSummary({ queue: [{ status: 'queued' }] }), '1 request awaiting approval');
  assert.equal(privSummary({ queue: [] }), '');
});

test('agent summary separates a claim on your attention from reassurance', () => {
  const state = { rows: [{ state: 'needs-input' }, { state: 'working' }, { state: 'working' }], asks: [] };
  assert.equal(agentSummary(state), '1 waiting on you · 2 agents working');
  assert.equal(agentSummary({ rows: [{ state: 'working' }], asks: [] }), '1 agent working');
  assert.equal(agentSummary({ rows: [], asks: [{ id: 'q' }] }), '1 waiting on you');
  assert.equal(agentSummary({ rows: [], asks: [] }), '');
});

test('net summary speaks up for the states that matter, not the healthy one', () => {
  assert.equal(netSummary({ status: 'await-confirm' }), 'awaiting confirmation');
  assert.equal(netSummary({ status: 'failed' }), 'failed');
  assert.equal(netSummary({ status: 'committed' }), '');
  assert.equal(netSummary(null), '');
});

test('host rows exclude LOCAL — the seat is the section body, not a group', () => {
  const m = mk({
    local: { notify: { notifications: [{ read: false }] } },
    build01: { notify: { notifications: [{ read: false }] } },
  });
  const rows = remoteHostRows(m, SERVICE_NOTIFY, countBadge(unreadNotifications), notifySummary, upEverywhere);
  assert.deepEqual(rows.map((r) => r.origin), ['build01']);
});

test('a quiet remote host earns no row', () => {
  const m = mk({ build01: { notify: { notifications: [{ read: true }] } } });
  assert.deepEqual(remoteHostRows(m, SERVICE_NOTIFY, countBadge(unreadNotifications), notifySummary, upEverywhere), []);
});

test('a down host is dropped: its counts are no longer about anything', () => {
  const m = mk({
    build01: { notify: { notifications: [{ read: false }] } },
    gone02: { notify: { notifications: [{ read: false }] } },
  });
  const rows = remoteHostRows(
    m, SERVICE_NOTIFY, countBadge(unreadNotifications), notifySummary,
    (o) => (o === 'gone02' ? 'down' : 'up'),
  );
  assert.deepEqual(rows.map((r) => r.origin), ['build01']);
});

test('a reconnecting host is KEPT, carrying its status for the greying', () => {
  const m = mk({ build01: { notify: { notifications: [{ read: false }] } } });
  const rows = remoteHostRows(
    m, SERVICE_NOTIFY, countBadge(unreadNotifications), notifySummary, () => 'reconnecting',
  );
  assert.deepEqual(rows, [{ origin: 'build01', badge: '1', summary: '1 unread', status: 'reconnecting' }]);
});

test('net rows appear on a verb, with no count to speak of', () => {
  const m = mk({ build01: { net: { status: 'await-confirm' } }, build02: { net: { status: 'committed' } } });
  const rows = remoteHostRows(m, SERVICE_NET, netBadgeForHost, netSummary, upEverywhere);
  assert.deepEqual(rows, [
    { origin: 'build01', badge: '!', summary: 'awaiting confirmation', status: 'up' },
  ]);
});

test('service keys map to their section ids, agent → agents included', () => {
  assert.equal(sectionForService(SERVICE_NOTIFY), 'notify');
  assert.equal(sectionForService(SERVICE_NET), 'net');
  assert.equal(sectionForService(SERVICE_AGENT), 'agents');
});

test('net urgency fires only for the lock-out window', () => {
  // A failure is already over: it earns a badge, not a section popping open.
  assert.equal(netUrgency({ status: 'await-confirm' }), 1);
  assert.equal(netUrgency({ status: 'failed' }), 0);
  assert.equal(netUrgency({ status: 'committed' }), 0);
  assert.equal(netUrgency(null), 0);
});

test('every watched counter has a section to expand', () => {
  for (const [service] of AWARENESS_COUNTERS) {
    assert.ok(sectionForService(service).length > 0, `no section for ${service}`);
  }
  // And the five awareness services are all covered — a missing entry
  // means that service never auto-expands anything.
  assert.deepEqual(
    AWARENESS_COUNTERS.map(([s]) => s).sort(),
    [SERVICE_AGENT, SERVICE_BULK, SERVICE_NET, SERVICE_NOTIFY, SERVICE_PRIV].sort(),
  );
});

test('agent host summary includes a host whose agents are merely busy', () => {
  // Keying the door off "waiting" alone left a working agent unreachable.
  const m = mk({
    local: { agent: { rows: [], asks: [] } },
    busy01: { agent: { rows: [{ state: 'working' }], asks: [] } },
    blocked02: { agent: { rows: [{ state: 'needs-input' }], asks: [] } },
  });
  // Remotes are alphabetical, as everywhere else in the rail — a host
  // must not jump around because another host got busy.
  assert.deepEqual(agentHostSummary(m), [
    { origin: 'blocked02', waiting: 1, running: 1 },
    { origin: 'busy01', waiting: 0, running: 1 },
  ]);
});

test('agent host summary skips hosts with nothing at all', () => {
  const m = mk({ quiet: { agent: { rows: [], asks: [] } } });
  assert.deepEqual(agentHostSummary(m), []);
});

test('running counts every live session, blocked or not', () => {
  assert.equal(runningAgents({ rows: [{ state: 'working' }, { state: 'needs-input' }] }), 2);
  assert.equal(runningAgents({}), 0);
});
