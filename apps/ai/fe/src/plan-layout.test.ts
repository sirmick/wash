import { test } from 'node:test';
import assert from 'node:assert/strict';
import { MAX_ROWS, layoutPlan } from './plan-layout.ts';

const shakedown = [
  { id: 'M1', template: 'milestone' },
  { id: 'M2', template: 'milestone', needs: ['M1'] },
  { id: 'M3', template: 'milestone', needs: ['M2'] },
  { id: 'A', parent: 'M2' },
  { id: 'B', parent: 'M2' },
  { id: 'C', parent: 'M2', needs: ['A', 'B'] },
  { id: 'C1', parent: 'C' },
];

const inside = (inner: { x: number; y: number; w: number; h: number }, outer: { x: number; y: number; w: number; h: number }) =>
  inner.x >= outer.x && inner.x + inner.w <= outer.x + outer.w && inner.y >= outer.y && inner.y + inner.h <= outer.y + outer.h;

test('milestones are columns in need order, their nodes in level boxes inside them', () => {
  const l = layoutPlan(shakedown);
  const box = (id: string) => l.boxes.find((b) => b.id === id)!;
  assert.ok(box('M1').x < box('M2').x && box('M2').x < box('M3').x, 'columns follow needs');
  assert.equal(box('M1').sketch, true);
  assert.equal(box('M3').sketch, true);
  assert.equal(box('M2').kind, 'group');
  assert.equal(box('M2').sketch, undefined);
  for (const id of ['A', 'B', 'C', 'M2/0', 'M2/1']) assert.ok(inside(box(id), box('M2')), `${id} inside M2`);
  assert.equal(box('M2/0').kind, 'level');
  assert.deepEqual([box('M2/0').level, box('M2/0').count], [0, 2], 'level 0 holds the two nodes that need nothing');
  assert.deepEqual([box('M2/1').level, box('M2/1').count], [1, 1]);
  assert.ok(inside(box('A'), box('M2/0')) && inside(box('B'), box('M2/0')), 'A and B sit in level 0');
  assert.ok(inside(box('C'), box('M2/1')), 'C sits in level 1');
  assert.equal(box('A').x, box('B').x, 'A and B share a sub-column');
  assert.ok(box('C').x > box('A').x, 'C comes after what it needs');
  assert.equal(l.boxes.some((b) => b.id === 'C1'), false, 'a step is listed inside its package, not boxed');
  assert.ok(box('C').h > box('A').h, 'C is taller for its step');
});

test('edges join milestones, and each level to the next; needs inside a group are the level arrow', () => {
  const l = layoutPlan(shakedown);
  const pairs = l.edges.map((e) => `${e.from}>${e.to}`).sort();
  assert.deepEqual(pairs, ['M1>M2', 'M2/0>M2/1', 'M2>M3']);
  for (const e of l.edges) assert.match(e.d, /^M[\d.]+,[\d.]+ C/);
});

test('every need is a needEdge between the boxes for its ends, for drawing a picked node\'s own', () => {
  const l = layoutPlan(shakedown);
  const pairs = l.needEdges.map((e) => `${e.from}>${e.to}`).sort();
  assert.deepEqual(pairs, ['A>C', 'B>C', 'M1>M2', 'M2>M3']);
});

test('a need on a step inside another package ranks past that package', () => {
  const l = layoutPlan([...shakedown, { id: 'D', parent: 'M2', needs: ['C1'] }]);
  const box = (id: string) => l.boxes.find((b) => b.id === id)!;
  assert.ok(box('D').x > box('C').x);
  assert.ok(l.edges.some((e) => e.from === 'M2/1' && e.to === 'M2/2'));
  assert.ok(l.needEdges.some((e) => e.from === 'C' && e.to === 'D'), 'the step\'s need is its package\'s');
});

test('a need across milestones is drawn node to node, so the dependency is visible', () => {
  const l = layoutPlan([...shakedown, { id: 'E', parent: 'M3', needs: ['A'] }]);
  assert.ok(l.edges.some((e) => e.from === 'A' && e.to === 'E'));
});

test('a level with many independent nodes wraps into sub-columns', () => {
  const many = Array.from({ length: 25 }, (_, i) => ({ id: `N${i}`, parent: 'M' }));
  const l = layoutPlan([{ id: 'M', template: 'milestone' }, ...many]);
  const nodes = l.boxes.filter((b) => b.kind === 'node');
  const xs = new Set(nodes.map((b) => b.x));
  assert.equal(xs.size, Math.ceil(25 / MAX_ROWS), 'three sub-columns for 25 nodes');
  for (const x of xs) assert.ok(nodes.filter((b) => b.x === x).length <= MAX_ROWS);
  const level = l.boxes.find((b) => b.id === 'M/0')!;
  for (const n of nodes) assert.ok(inside(n, level), `${n.id} inside the level box`);
  assert.equal(level.count, 25);
});

test('a node sits nearest what it needs: levels are ordered by the barycenter of their needs', () => {
  const plan = [
    { id: 'M', template: 'milestone' },
    { id: 'P', parent: 'M' }, { id: 'Q', parent: 'M' }, { id: 'R', parent: 'M' },
    // Declared in the opposite order of what they need.
    { id: 'r1', parent: 'M', needs: ['R'] }, { id: 'q1', parent: 'M', needs: ['Q'] }, { id: 'p1', parent: 'M', needs: ['P'] },
  ];
  const l = layoutPlan(plan);
  const y = (id: string) => l.boxes.find((b) => b.id === id)!.y;
  assert.ok(y('P') < y('Q') && y('Q') < y('R'), 'level 0 keeps plan order');
  assert.ok(y('p1') < y('q1') && y('q1') < y('r1'), 'level 1 follows the places of its needs');
});

test('done work folds away: a done milestone to its header, done nodes to one chip per level', () => {
  const plan = [
    { id: 'M1', template: 'milestone', state: 'done' },
    { id: 'a', parent: 'M1', state: 'done' }, { id: 'b', parent: 'M1', state: 'done', needs: ['a'] },
    { id: 'M2', template: 'milestone', state: 'active', needs: ['M1'] },
    { id: 'c', parent: 'M2', state: 'done' }, { id: 'd', parent: 'M2', state: 'done' }, { id: 'e', parent: 'M2', state: 'active' },
    { id: 'f', parent: 'M2', state: 'active', needs: ['c', 'e'] },
    { id: 'g', parent: 'M2', state: 'todo', needs: ['b'] },
  ];
  const l = layoutPlan(plan);
  const box = (id: string) => l.boxes.find((b) => b.id === id);
  assert.equal(box('M1')!.collapsed, true);
  assert.equal(box('M1')!.count, 2);
  assert.equal(box('a'), undefined, 'nothing inside a collapsed milestone is boxed');
  const chip = box('M2/0/done')!;
  assert.equal(chip.kind, 'done');
  assert.equal(chip.count, 2, 'c and d fold into one chip');
  assert.equal(box('c'), undefined);
  assert.ok(box('e') && box('f'), 'open nodes stay');
  assert.ok(inside(chip, box('M2/0')!), 'the chip sits in its level');
  assert.equal(box('M2/0')!.count, 4, 'the level still counts every node: c, d, e and g (whose need is in another milestone)');
  const pairs = l.needEdges.map((e) => `${e.from}>${e.to}`).sort();
  assert.deepEqual(pairs, ['M1>M2', 'M1>g', 'M2/0/done>f', 'e>f'], 'needs on folded nodes land on what stands for them');
  const open = layoutPlan(plan, () => 0, { collapseDone: false });
  assert.ok(open.boxes.some((b) => b.id === 'a') && open.boxes.some((b) => b.id === 'c'), 'shown in full on request');
  assert.equal(open.boxes.some((b) => b.kind === 'done'), false);
});

test('extra lines make a box taller, and the layout survives a cycle it is given', () => {
  const plain = layoutPlan([{ id: 'X' }]).boxes[0];
  const tall = layoutPlan([{ id: 'X' }], () => 3).boxes[0];
  assert.ok(tall.h > plain.h);
  const cyclic = layoutPlan([{ id: 'P', needs: ['Q'] }, { id: 'Q', needs: ['P'] }]);
  assert.equal(cyclic.boxes.length, 2);
});
