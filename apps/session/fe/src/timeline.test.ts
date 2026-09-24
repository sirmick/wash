import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import {
  familyOf, filterEntries, groupByHour, hourLabel, kindIcon, kindLabel, mergeNewestFirst, prependLive, rowText,
} from './timeline.ts';

const e = (over: Partial<WashActivityEntry> = {}): WashActivityEntry => ({
  ts: Date.UTC(2026, 8, 16, 14, 30), seq: 1, host: 'local', kind: 'window.open', line: 'Terminal', ...over,
});

test('kinds fold into the families a person filters by', () => {
  assert.equal(familyOf('window.focus'), 'windows');
  assert.equal(familyOf('agent.turn'), 'agents');
  assert.equal(familyOf('rollup'), 'agents');
  assert.equal(familyOf('open.routed'), 'files');
  assert.equal(familyOf('bulk.done'), 'files');
  assert.equal(familyOf('session.attach'), 'hosts');
  assert.equal(familyOf('peer.down'), 'hosts');
  assert.equal(familyOf('note'), 'other');
});

test('every kind has an icon and a label, and unknown kinds degrade gracefully', () => {
  for (const k of ['window.open', 'agent.turn', 'open.routed', 'peer.up', 'brief']) {
    assert.ok(kindIcon(k).length > 0, k);
    assert.notEqual(kindLabel(k), k, k);
  }
  assert.equal(kindIcon('something.new'), 'info');
  assert.equal(kindLabel('something.new'), 'something.new');
});

test('entries group by local hour, newest first, and the label says which day', () => {
  const t = new Date(2026, 8, 16, 14, 30).getTime();
  const groups = groupByHour([
    e({ ts: t + 60_000, seq: 3 }), e({ ts: t, seq: 2 }), e({ ts: t - 3_600_000, seq: 1 }),
  ], new Date(2026, 8, 16, 18, 0).getTime());
  assert.equal(groups.length, 2);
  assert.equal(groups[0].rows.length, 2);
  assert.equal(groups[0].label, 'Today 14:00');
  assert.equal(groups[1].label, 'Today 13:00');
  assert.equal(hourLabel(new Date(2026, 8, 15, 9, 0).getTime(), new Date(2026, 8, 16, 18, 0).getTime()), 'Yesterday 09:00');
});

test('a filter keeps one family and matches text across title, line, app and host', () => {
  const rows = [
    e({ seq: 1, kind: 'window.open', title: 'Terminal', line: 'Terminal' }),
    e({ seq: 2, kind: 'agent.turn', title: 'Fix the banner', line: 'turn 3 done', app: 'com.wash.agentd' }),
    e({ seq: 3, kind: 'peer.up', title: 'build01', line: 'remote host build01 connected', host: 'build01' }),
  ];
  assert.deepEqual(filterEntries(rows, 'agents', '').map((r) => r.seq), [2]);
  assert.deepEqual(filterEntries(rows, 'all', 'BUILD01').map((r) => r.seq), [3]);
  assert.deepEqual(filterEntries(rows, 'all', 'agentd').map((r) => r.seq), [2]);
  assert.equal(filterEntries(rows, 'files', '').length, 0);
});

test('pages from several hosts merge newest first without duplicates, and live rows prepend once', () => {
  const merged = mergeNewestFirst([
    { entries: [e({ ts: 30, seq: 2 }), e({ ts: 10, seq: 1 })] },
    { entries: [e({ ts: 20, seq: 1, host: 'b' }), e({ ts: 10, seq: 1 })] },
  ]);
  assert.deepEqual(merged.map((r) => `${r.host}:${r.seq}@${r.ts}`), ['local:2@30', 'b:1@20', 'local:1@10']);
  const live = prependLive(merged, e({ ts: 40, seq: 3 }));
  assert.equal(live[0].seq, 3);
  assert.equal(prependLive(live, e({ ts: 40, seq: 3 })).length, live.length);
});

test('a row shows its title with the line beneath, or just the line', () => {
  assert.deepEqual(rowText(e({ title: 'Fix it', line: 'turn 3 done' })), { main: 'Fix it', sub: 'turn 3 done' });
  assert.deepEqual(rowText(e({ title: 'Terminal', line: 'Terminal' })), { main: 'Terminal', sub: '' });
  assert.deepEqual(rowText(e({ title: undefined, line: 'browser connected' })), { main: 'browser connected', sub: '' });
});
