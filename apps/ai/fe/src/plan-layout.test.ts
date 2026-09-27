import { test } from 'node:test';
import assert from 'node:assert/strict';
import { layoutPlan } from './plan-layout.ts';

const shakedown = [
  { id: 'M1', template: 'milestone' },
  { id: 'M2', template: 'milestone', needs: ['M1'] },
  { id: 'M3', template: 'milestone', needs: ['M2'] },
  { id: 'A', parent: 'M2' },
  { id: 'B', parent: 'M2' },
  { id: 'C', parent: 'M2', needs: ['A', 'B'] },
  { id: 'C1', parent: 'C' },
];

test('milestones are columns in need order, their nodes in layers inside them', () => {
  const l = layoutPlan(shakedown);
  const box = (id: string) => l.boxes.find((b) => b.id === id)!;
  assert.ok(box('M1').x < box('M2').x && box('M2').x < box('M3').x, 'columns follow needs');
  assert.equal(box('M1').sketch, true);
  assert.equal(box('M3').sketch, true);
  assert.equal(box('M2').kind, 'group');
  assert.equal(box('M2').sketch, undefined);
  for (const id of ['A', 'B', 'C']) {
    const b = box(id);
    assert.ok(b.x >= box('M2').x && b.x + b.w <= box('M2').x + box('M2').w, `${id} inside M2`);
    assert.ok(b.y >= box('M2').y && b.y + b.h <= box('M2').y + box('M2').h, `${id} inside M2 vertically`);
  }
  assert.equal(box('A').x, box('B').x, 'A and B share a layer');
  assert.ok(box('C').x > box('A').x, 'C comes after what it needs');
  assert.equal(l.boxes.some((b) => b.id === 'C1'), false, 'a step is listed inside its package, not boxed');
  assert.ok(box('C').h > box('A').h, 'C is taller for its step');
});

test('edges join needs to what needs them, once, and nothing else', () => {
  const l = layoutPlan(shakedown);
  const pairs = l.edges.map((e) => `${e.from}>${e.to}`).sort();
  assert.deepEqual(pairs, ['A>C', 'B>C', 'M1>M2', 'M2>M3']);
  for (const e of l.edges) assert.match(e.d, /^M[\d.]+,[\d.]+ C/);
});

test('a need on a step inside another package points at that package', () => {
  const l = layoutPlan([...shakedown, { id: 'D', parent: 'M2', needs: ['C1'] }]);
  assert.ok(l.edges.some((e) => e.from === 'C' && e.to === 'D'));
});

test('extra lines make a box taller, and the layout survives a cycle it is given', () => {
  const plain = layoutPlan([{ id: 'X' }]).boxes[0];
  const tall = layoutPlan([{ id: 'X' }], () => 3).boxes[0];
  assert.ok(tall.h > plain.h);
  const cyclic = layoutPlan([{ id: 'P', needs: ['Q'] }, { id: 'Q', needs: ['P'] }]);
  assert.equal(cyclic.boxes.length, 2);
});
