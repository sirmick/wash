// Unit tests for the generic split kernel (docs/WM_LAYOUT.md §2, §3). The
// tab-group behaviour on top of it is covered by apps/term/fe/src/layout.test.ts;
// these pin what the window manager relies on — leaves that survive empty,
// and the layout syntax.

import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  LayoutSyntaxError,
  formatLayout,
  leafPaths,
  normalize,
  parseLayout,
  place,
  resizeSplit,
} from './split-tree.ts';
import type { LeafOps, Node, Split } from './split-tree.ts';

interface Slot { kind: 'slot' }
const slot = (): Slot => ({ kind: 'slot' });
const SLOT_OPS: LeafOps<Slot> = { empty: slot };

const parse = (s: string) => parseLayout(s, slot);
const roundTrip = (s: string) => formatLayout(parse(s));

const sizesOf = (n: Node<Slot>) => (n as Split<Slot>).sizes.map((f) => Math.round(f * 1000) / 10);

test('empty leaves survive normalize when keep is not given', () => {
  const t: Node<Slot> = { kind: 'split', dir: 'row', children: [slot(), slot()], sizes: [0.5, 0.5] };
  assert.equal(leafPaths(normalize(t, SLOT_OPS)).length, 2);
});

test('the presets parse to the shapes they name', () => {
  assert.equal(leafPaths(parse('1')).length, 1);
  assert.equal(leafPaths(parse('r3')).length, 3);
  assert.equal(leafPaths(parse('r[c2 c2]')).length, 4);
  assert.equal(leafPaths(parse('r[c4 c4 c4 c4]')).length, 16);
  const top = parse('c[1 r2]') as Split<Slot>;
  assert.equal(top.dir, 'col');
  assert.equal((top.children[1] as Split<Slot>).dir, 'row');
});

test('unsized items share what the sized ones leave', () => {
  assert.deepEqual(sizesOf(parse('r[1@25 1 1@25]')), [25, 50, 25]);
  assert.deepEqual(sizesOf(parse('r[1@40 1 1]')), [40, 30, 30]);
});

test('sizes that do not come to 100 are scaled, never refused', () => {
  assert.deepEqual(sizesOf(parse('r[1@1 1@3]')), [25, 75]);
  assert.deepEqual(sizesOf(parse('r[1@80 1@80]')), [50, 50]);
  // Over 100 with an unsized item: everyone is treated as even, then scaled.
  assert.deepEqual(sizesOf(parse('r[1@150 1]')), [75, 25]);
});

test('same-direction nesting flattens, so a layout has one form', () => {
  // Flattening keeps proportions: the inner pair halves its parent's half.
  assert.equal(roundTrip('r[r2 1]'), 'r[1@25 1@25 1@50]');
  assert.equal(roundTrip('r[r2 r2]'), 'r4');
  assert.equal(roundTrip('r1'), '1');
  assert.equal(roundTrip('c[1]'), '1');
  assert.equal(roundTrip('r[c1 c1]'), 'r2');
});

test('formatLayout is canonical and round-trips', () => {
  const cases: Array<[string, string]> = [
    ['1', '1'],
    ['r2', 'r2'],
    ['r[1 1]', 'r2'],
    ['r[c2 c2]', 'r[c2 c2]'],
    ['r[1@25 1 1@25]', 'r[1@25 1@50 1@25]'],
    ['  c[ 1   r2 ] ', 'c[1 r2]'],
    ['r[c[1@70 1]@60 1]', 'r[c[1@70 1@30]@60 1@40]'],
    ['r3', 'r3'],
  ];
  for (const [src, want] of cases) {
    assert.equal(roundTrip(src), want, src);
    assert.equal(roundTrip(want), want, `${want} is a fixed point`);
  }
});

test('a divider drag formats back to tenths of a percent', () => {
  const t = resizeSplit(parse('r2'), '', 0, 0.12345, SLOT_OPS);
  assert.equal(formatLayout(t), 'r[1@62.3 1@37.7]');
  assert.equal(roundTrip(formatLayout(t)), formatLayout(t));
});

test('place gives each leaf its fraction of the rect', () => {
  const { leaves, dividers } = place(parse('r[1@25 1 1@25]'), { x: 0, y: 0, w: 1000, h: 500 }, 0);
  assert.deepEqual(leaves.map((l) => l.rect.w), [250, 500, 250]);
  assert.equal(dividers.length, 2);
});

test('malformed strings are refused with a position', () => {
  for (const bad of ['', 'x', 'r', 'r0', 'r[', 'r[]', 'r[1@]', 'r[1@0]', 'r2 1', 'r[1 1', 'r65']) {
    assert.throws(() => parse(bad), LayoutSyntaxError, JSON.stringify(bad));
  }
  assert.throws(() => parse('r[1 x]'), (e: LayoutSyntaxError) => e.at === 4);
});
