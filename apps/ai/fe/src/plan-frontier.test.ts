import { test } from 'node:test';
import assert from 'node:assert/strict';
import { frontierText, planFrontier } from './plan-frontier.ts';

test('a fat start: independent nodes are one wide wave, then the chain behind them', () => {
  const plan = [
    { id: 'M', template: 'milestone', state: 'active' },
    { id: 'a', parent: 'M', state: 'todo' }, { id: 'b', parent: 'M', state: 'todo' }, { id: 'c', parent: 'M', state: 'todo' },
    { id: 'd', parent: 'M', state: 'todo', needs: ['a', 'b'] },
    { id: 'e', parent: 'M', state: 'todo', needs: ['d'] },
    { id: 'e1', parent: 'e', state: 'todo', needs: ['c'] }, // a step's need is its package's
  ];
  const f = planFrontier(plan);
  assert.deepEqual(f.waves, [3, 1, 1]);
  assert.equal(f.remaining, 5);
  assert.equal(f.narrow, false, 'three wide now');
  assert.match(frontierText(f), /^ahead: 3 → 1 → 1 \(5 left\)$/);
});

test('the tail: done work is out of the count and a two-wide chain reads as narrow', () => {
  const plan = [
    { id: 'M1', template: 'milestone', state: 'done' }, { id: 'x', parent: 'M1', state: 'done' },
    { id: 'M2', template: 'milestone', state: 'active', needs: ['M1'] },
    { id: 'p', parent: 'M2', state: 'done' }, { id: 'q', parent: 'M2', state: 'done' },
    { id: 'r', parent: 'M2', state: 'active', needs: ['p'] }, { id: 's', parent: 'M2', state: 'reported', needs: ['q'] },
    { id: 't', parent: 'M2', state: 'todo', needs: ['r', 's'] },
  ];
  const f = planFrontier(plan);
  assert.deepEqual(f.waves, [2, 1]);
  assert.equal(f.remaining, 3);
  assert.equal(f.narrow, true);
  assert.match(frontierText(f), /narrow: one agent could close this$/);
});

test('an unplanned milestone makes the width unknown, so never narrow; a resolved QA need unblocks', () => {
  const plan = [
    { id: 'M2', template: 'milestone', state: 'active' },
    { id: 'r', parent: 'M2', state: 'active', needs: ['Q-thread'] },
    { id: 'M3', template: 'milestone', state: 'todo', needs: ['M2'] },
  ];
  const open = planFrontier(plan, (need) => need === 'Q-thread');
  assert.deepEqual(open.waves, [0, 1], 'blocked on the thread now, free after');
  assert.deepEqual(open.unplanned, ['M3']);
  assert.equal(open.narrow, false);
  assert.match(frontierText(open), /1 milestone unplanned/);
  const resolved = planFrontier(plan);
  assert.deepEqual(resolved.waves, [1]);
});

test('a milestone frees what needs it once its last node is done, and everything done says so', () => {
  const plan = [
    { id: 'M1', template: 'milestone', state: 'active' }, { id: 'a', parent: 'M1', state: 'todo' },
    { id: 'M2', template: 'milestone', state: 'todo', needs: ['M1'] }, { id: 'b', parent: 'M2', state: 'todo' },
  ];
  assert.deepEqual(planFrontier(plan).waves, [1, 1]);
  const done = plan.map((n) => ({ ...n, state: 'done' }));
  assert.equal(frontierText(planFrontier(done)), 'all done');
});
