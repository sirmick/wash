import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { filterSortProcs, type SortableProc } from './procsort.ts';

const p = (over: Partial<SortableProc> = {}): SortableProc => ({
  PID: 1,
  Comm: 'init',
  Cmd: '/sbin/init',
  User: 'root',
  CPU: 0,
  RSS: 0,
  TimeJiff: 0,
  ...over,
});

const ids = (rows: SortableProc[]) => rows.map((r) => r.PID);

test('sorts by cpu descending / ascending', () => {
  const rows = [p({ PID: 1, CPU: 10 }), p({ PID: 2, CPU: 90 }), p({ PID: 3, CPU: 50 })];
  assert.deepEqual(ids(filterSortProcs(rows, '', 'cpu', true)), [2, 3, 1]);
  assert.deepEqual(ids(filterSortProcs(rows, '', 'cpu', false)), [1, 3, 2]);
});

test('sorts by mem (RSS), pid, and time', () => {
  const rows = [p({ PID: 1, RSS: 100, TimeJiff: 5 }), p({ PID: 2, RSS: 300, TimeJiff: 1 }), p({ PID: 3, RSS: 200, TimeJiff: 9 })];
  assert.deepEqual(ids(filterSortProcs(rows, '', 'mem', true)), [2, 3, 1]);
  assert.deepEqual(ids(filterSortProcs(rows, '', 'pid', false)), [1, 2, 3]);
  assert.deepEqual(ids(filterSortProcs(rows, '', 'time', true)), [3, 1, 2]);
});

test('sorts by user/cmd via localeCompare', () => {
  const rows = [p({ PID: 1, User: 'zoe', Cmd: 'zsh' }), p({ PID: 2, User: 'ann', Cmd: 'apt' })];
  assert.deepEqual(ids(filterSortProcs(rows, '', 'user', false)), [2, 1]);
  assert.deepEqual(ids(filterSortProcs(rows, '', 'cmd', false)), [2, 1]);
});

test('filter matches cmd/comm/user case-insensitively', () => {
  const rows = [p({ PID: 1, Cmd: '/usr/bin/SSHD' }), p({ PID: 2, Comm: 'bash' }), p({ PID: 3, User: 'Postgres' })];
  assert.deepEqual(ids(filterSortProcs(rows, 'sshd', 'pid', false)), [1]);
  assert.deepEqual(ids(filterSortProcs(rows, 'BASH', 'pid', false)), [2]);
  assert.deepEqual(ids(filterSortProcs(rows, 'postgres', 'pid', false)), [3]);
});

test('filter matches the PID as a string substring', () => {
  const rows = [p({ PID: 1234 }), p({ PID: 5678 }), p({ PID: 234 })];
  assert.deepEqual(ids(filterSortProcs(rows, '234', 'pid', false)), [234, 1234]);
});

test('blank/whitespace filter returns everything (just sorted)', () => {
  const rows = [p({ PID: 2, CPU: 1 }), p({ PID: 1, CPU: 2 })];
  assert.deepEqual(ids(filterSortProcs(rows, '   ', 'cpu', true)), [1, 2]);
});

test('does not mutate the input array', () => {
  const rows = [p({ PID: 1, CPU: 1 }), p({ PID: 2, CPU: 9 })];
  const before = ids(rows);
  filterSortProcs(rows, '', 'cpu', true);
  assert.deepEqual(ids(rows), before, 'input order preserved');
});
