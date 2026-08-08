// Unit tests for the split-layout kernel (docs/TERM_LAYOUT.md §9).
// Pure logic — no Solid, no DOM, so plain `node --test` runs it.

import { test } from 'node:test';
import assert from 'node:assert/strict';

import {
  ROOT,
  addTab,
  canSplit,
  channels,
  closeTab,
  equalize,
  equalizeAll,
  focusNeighbor,
  fromPersisted,
  groupAt,
  groupPaths,
  layout,
  minFractionFor,
  moveTabBefore,
  normalize,
  pathOfChannel,
  pruneToChannels,
  resizeSplit,
  setActiveTab,
  singleGroup,
  splitGroup,
  toPersisted,
  visibleChannels,
} from './layout.ts';
import type { LayoutNode, Rect, Split } from './layout.ts';

const STAGE: Rect = { x: 0, y: 0, w: 1000, h: 600 };

// sizesSumToOne is the invariant every mutation must preserve.
function assertInvariants(tree: LayoutNode, where: string) {
  const seen = new Set<number>();
  const walk = (n: LayoutNode) => {
    if (n.kind === 'group') {
      assert.ok(
        n.tabs.length === 0 || n.tabs.includes(n.active),
        `${where}: group.active ${n.active} not in [${n.tabs}]`,
      );
      for (const c of n.tabs) {
        assert.ok(!seen.has(c), `${where}: channel ${c} appears twice`);
        seen.add(c);
      }
      return;
    }
    assert.ok(n.children.length >= 2, `${where}: split with ${n.children.length} children`);
    assert.equal(n.sizes.length, n.children.length, `${where}: sizes/children length`);
    const sum = n.sizes.reduce((a, b) => a + b, 0);
    assert.ok(Math.abs(sum - 1) < 1e-6, `${where}: sizes sum ${sum}`);
    for (const s of n.sizes) assert.ok(s > 0, `${where}: non-positive fraction ${s}`);
    for (const c of n.children) {
      assert.ok(
        !(c.kind === 'split' && c.dir === n.dir),
        `${where}: same-direction split not flattened`,
      );
      walk(c);
    }
  };
  walk(tree);
}

test('an unsplit window is one group holding every tab', () => {
  const t = singleGroup([101, 102, 103], 102);
  assertInvariants(t, 'single');
  assert.deepEqual(groupPaths(t), [ROOT]);
  assert.deepEqual(channels(t), [101, 102, 103]);
  assert.deepEqual(visibleChannels(t), [102]);
});

test('singleGroup repairs an active that is not a member', () => {
  assert.equal(singleGroup([1, 2], 99).active, 1);
  assert.equal(singleGroup([], 99).active, 0);
});

test('splitGroup puts the new channel in the new half', () => {
  const t = splitGroup(singleGroup([101]), ROOT, 'row', 102);
  assertInvariants(t, 'split');
  assert.equal(t.kind, 'split');
  const s = t as Split;
  assert.equal(s.dir, 'row');
  assert.deepEqual(s.sizes, [0.5, 0.5]);
  assert.deepEqual(groupAt(t, '0')!.tabs, [101]);
  assert.deepEqual(groupAt(t, '1')!.tabs, [102]);
  assert.equal(groupAt(t, '1')!.active, 102);
});

test('same-direction splits flatten into one n-ary split', () => {
  // Split right three times: one row of three, not a lopsided binary chain.
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'row', 3);
  assertInvariants(t, 'flatten');
  assert.equal(t.kind, 'split');
  const s = t as Split;
  assert.equal(s.children.length, 3, 'three siblings, not nesting');
  assert.deepEqual(s.children.map((c) => (c.kind === 'group' ? c.tabs : null)), [[1], [2], [3]]);
  // The split half keeps its share: 0.5 then 0.25/0.25.
  assert.deepEqual(s.sizes.map((n) => Math.round(n * 100) / 100), [0.5, 0.25, 0.25]);
});

test('a cross-direction split nests instead of flattening', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'col', 3);
  assertInvariants(t, 'nest');
  const s = t as Split;
  assert.equal(s.children.length, 2);
  assert.equal(s.children[1].kind, 'split');
  assert.equal((s.children[1] as Split).dir, 'col');
});

test('closing the last tab of a group collapses it and gives back the space', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'col', 3);
  // Kill the pane that made the nested column.
  t = closeTab(t, 3);
  assertInvariants(t, 'collapse');
  const s = t as Split;
  assert.equal(s.children.length, 2, 'the 1-child column collapsed into its group');
  assert.deepEqual(s.children.map((c) => (c.kind === 'group' ? c.tabs : null)), [[1], [2]]);
  // Closing again leaves a bare group — no 1-child split.
  t = closeTab(t, 2);
  assertInvariants(t, 'collapse2');
  assert.equal(t.kind, 'group');
  assert.deepEqual(channels(t), [1]);
});

test('closing the active tab activates its right neighbour, then its left', () => {
  let t: LayoutNode = singleGroup([1, 2, 3], 2);
  t = closeTab(t, 2);
  assert.equal(groupAt(t, ROOT)!.active, 3, 'follows the tab to the right');
  t = closeTab(t, 3);
  assert.equal(groupAt(t, ROOT)!.active, 1, 'then falls back to the left');
  t = closeTab(t, 1);
  assert.deepEqual(channels(t), []);
});

test('closing an inactive tab leaves activation alone', () => {
  const t = closeTab(singleGroup([1, 2, 3], 2), 3);
  assert.equal(groupAt(t, ROOT)!.active, 2);
});

test('sizes redistribute proportionally when a sibling dies', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'row', 3);            // 0.5 / 0.25 / 0.25
  t = closeTab(t, 1);                          // drop the big one
  assertInvariants(t, 'redistribute');
  const s = t as Split;
  assert.equal(s.children.length, 2);
  assert.deepEqual(s.sizes, [0.5, 0.5], 'the remainder splits what is left in its own ratio');
});

test('addTab lands in the named group and takes focus there', () => {
  let t: LayoutNode = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  t = addTab(t, '0', 3);
  assertInvariants(t, 'addTab');
  assert.deepEqual(groupAt(t, '0')!.tabs, [1, 3]);
  assert.equal(groupAt(t, '0')!.active, 3);
  assert.equal(groupAt(t, '1')!.active, 2, 'the other group is untouched');
});

test('addTab is idempotent and survives a stale path', () => {
  const t = singleGroup([1]);
  assert.equal(addTab(t, ROOT, 1), t, 'a channel already in the tree is not added twice');
  const t2 = addTab(t, '7.7.7', 9);
  assert.deepEqual(channels(t2), [1, 9], 'a stale path falls back to the first group');
});

test('setActiveTab switches within the owning group only', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 1), ROOT, 'row', 3);
  t = setActiveTab(t, 2);
  assert.equal(groupAt(t, '0')!.active, 2);
  assert.equal(groupAt(t, '1')!.active, 3);
  assert.equal(setActiveTab(t, 404), t, 'an unknown channel is a no-op');
});

test('moveTabBefore reorders inside a group', () => {
  const t = moveTabBefore(singleGroup([1, 2, 3], 1), 3, 1);
  assertInvariants(t, 'reorder');
  assert.deepEqual(groupAt(t, ROOT)!.tabs, [3, 1, 2]);
});

test('moveTabBefore across groups moves the tab and collapses an emptied group', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 1), ROOT, 'row', 3);
  t = moveTabBefore(t, 3, 1);                  // drag the lone right tab into the left strip
  assertInvariants(t, 'cross-group move');
  assert.equal(t.kind, 'group', 'the emptied right group collapsed the split');
  assert.deepEqual(groupAt(t, ROOT)!.tabs, [3, 1, 2]);
  assert.equal(groupAt(t, ROOT)!.active, 3, 'the moved tab is active where it lands');
});

test('moving a tab out of a group re-activates a survivor', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 2), ROOT, 'row', 3);
  t = moveTabBefore(t, 2, 3);
  assertInvariants(t, 'move out');
  assert.equal(groupAt(t, '0')!.active, 1, 'the source group picked a survivor');
  assert.deepEqual(groupAt(t, '1')!.tabs, [2, 3]);
});

test('layout assigns disjoint rects and takes gutters off the span', () => {
  const t = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  const { groups, dividers } = layout(t, STAGE, { gutter: 4, strip: 24, status: 20 });
  assert.equal(groups.length, 2);
  assert.equal(dividers.length, 1);
  assert.equal(groups[0].rect.w, 498, '(1000 - 4) / 2');
  assert.equal(groups[1].rect.x, 502);
  assert.equal(dividers[0].rect.x, 498);
  assert.equal(dividers[0].rect.w, 4);
  // Content is the rect less the strip and status bar.
  assert.equal(groups[0].content.y, 24);
  assert.equal(groups[0].content.h, 556);
  assert.deepEqual(groups[0].status, { x: 0, y: 580, w: 498, h: 20 });
  // Disjoint: right edge of one is the divider, not the next pane.
  assert.ok(groups[0].rect.x + groups[0].rect.w <= groups[1].rect.x);
});

test('layout nests: a column inside a row', () => {
  let t: LayoutNode = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  t = splitGroup(t, '1', 'col', 3);
  const { groups } = layout(t, STAGE, { gutter: 4, strip: 24 });
  assert.equal(groups.length, 3);
  const [left, topRight, botRight] = groups;
  assert.equal(left.rect.h, 600, 'the left pane spans the full height');
  assert.equal(topRight.rect.x, botRight.rect.x, 'the right column shares an x');
  assert.equal(topRight.rect.h, 298, '(600 - 4) / 2');
  assert.equal(botRight.rect.y, 302);
});

test('canSplit refuses a split that would leave an unusable pane', () => {
  assert.equal(canSplit({ x: 0, y: 0, w: 1000, h: 600 }, 'row'), true);
  assert.equal(canSplit({ x: 0, y: 0, w: 200, h: 600 }, 'row'), false);
  assert.equal(canSplit({ x: 0, y: 0, w: 1000, h: 600 }, 'col'), true);
  assert.equal(canSplit({ x: 0, y: 0, w: 1000, h: 100 }, 'col'), false);
});

test('focusNeighbor follows geometry, not tree order', () => {
  // Left pane | right column of two. From the left pane, "right" must land
  // on the TOP right pane (it lines up with the centre), and tree order
  // would be just as happy handing back either.
  let t: LayoutNode = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  t = splitGroup(t, '1', 'col', 3);
  const { groups } = layout(t, STAGE, { gutter: 4, strip: 24 });
  const left = groups.find((g) => g.group.tabs.includes(1))!;
  const top = groups.find((g) => g.group.tabs.includes(2))!;
  const bottom = groups.find((g) => g.group.tabs.includes(3))!;

  assert.equal(focusNeighbor(groups, left.path, 'right'), top.path);
  assert.equal(focusNeighbor(groups, top.path, 'down'), bottom.path);
  assert.equal(focusNeighbor(groups, bottom.path, 'up'), top.path);
  assert.equal(focusNeighbor(groups, top.path, 'left'), left.path);
  assert.equal(focusNeighbor(groups, bottom.path, 'left'), left.path);
  assert.equal(focusNeighbor(groups, left.path, 'left'), undefined, 'nothing beyond the edge');
  assert.equal(focusNeighbor(groups, left.path, 'up'), undefined);
});

test('focusNeighbor picks the nearest pane, then the best aligned', () => {
  // Three columns: from the first, "right" is the middle one, never the last.
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'row', 3);
  const { groups } = layout(t, STAGE, { gutter: 4, strip: 24 });
  const first = groups.find((g) => g.group.tabs.includes(1))!;
  const middle = groups.find((g) => g.group.tabs.includes(2))!;
  assert.equal(focusNeighbor(groups, first.path, 'right'), middle.path);
});

test('resizeSplit trades between neighbours and clamps at the minimum', () => {
  const t = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  const bigger = resizeSplit(t, ROOT, 0, 0.2) as Split;
  assertInvariants(bigger, 'resize');
  assert.ok(Math.abs(bigger.sizes[0] - 0.7) < 1e-9);
  assert.ok(Math.abs(bigger.sizes[1] - 0.3) < 1e-9);
  const clamped = resizeSplit(t, ROOT, 0, -5, 0.1) as Split;
  assert.ok(Math.abs(clamped.sizes[0] - 0.1) < 1e-9, 'never collapses a pane to nothing');
});

test('equalize evens an n-ary split', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'row', 3);            // 0.5 / 0.25 / 0.25
  const even = equalize(t, ROOT) as Split;
  assertInvariants(even, 'equalize');
  for (const s of even.sizes) assert.ok(Math.abs(s - 1 / 3) < 1e-9);
});

test('pruneToChannels drops dead panes and repairs the tree', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 1), ROOT, 'row', 3);
  t = splitGroup(t, '1', 'col', 4);
  // Channels 3 and 4 died while the browser was away.
  const pruned = pruneToChannels(t, new Set([1, 2]));
  assertInvariants(pruned, 'prune');
  assert.equal(pruned.kind, 'group');
  assert.deepEqual(channels(pruned), [1, 2]);
});

test('pruneToChannels keeps activation live when the active tab dies', () => {
  const pruned = pruneToChannels(singleGroup([1, 2], 2), new Set([1]));
  assert.equal(groupAt(pruned, ROOT)!.active, 1);
});

test('everything survives a persist round trip', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 2), ROOT, 'row', 3);
  t = splitGroup(t, '1', 'col', 4);
  const back = fromPersisted(JSON.parse(JSON.stringify(toPersisted(t))));
  assert.deepEqual(back, t);
});

test('fromPersisted rejects junk so the caller can fall back to v1', () => {
  assert.equal(fromPersisted(null), undefined);
  assert.equal(fromPersisted({ kind: 'split', children: [] }), undefined);
  assert.equal(fromPersisted({ kind: 'nope' }), undefined);
  assert.equal(fromPersisted({ kind: 'group', tabs: [] }), undefined, 'an empty tree is not worth restoring');
  assert.equal(fromPersisted({ kind: 'split', children: [{ kind: 'group', tabs: [1] }, 7] }), undefined);
});

test('fromPersisted repairs missing or lopsided sizes', () => {
  const t = fromPersisted({
    kind: 'split',
    dir: 'row',
    children: [{ kind: 'group', tabs: [1], active: 1 }, { kind: 'group', tabs: [2], active: 2 }],
  }) as Split;
  assertInvariants(t, 'sizes repair');
  assert.deepEqual(t.sizes, [0.5, 0.5]);
});

test('a v1 blob migrates to one group in its saved order', () => {
  // What restoreFrom does with a pre-split saved state.
  const v1 = { tabs: [{ channel_id: 7 }, { channel_id: 8 }], active: 8 };
  const t = singleGroup(v1.tabs.map((r) => r.channel_id), v1.active);
  assertInvariants(t, 'migrate');
  assert.deepEqual(groupAt(t, ROOT)!.tabs, [7, 8]);
  assert.equal(groupAt(t, ROOT)!.active, 8);
});

test('pathOfChannel and groupPaths agree after nesting', () => {
  let t: LayoutNode = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  t = splitGroup(t, '1', 'col', 3);
  for (const c of channels(t)) {
    const p = pathOfChannel(t, c)!;
    assert.ok(groupPaths(t).includes(p));
    assert.ok(groupAt(t, p)!.tabs.includes(c));
  }
  assert.equal(pathOfChannel(t, 999), undefined);
});

test('normalize is idempotent', () => {
  let t: LayoutNode = splitGroup(singleGroup([1, 2], 1), ROOT, 'row', 3);
  t = splitGroup(t, '1', 'col', 4);
  assert.deepEqual(normalize(normalize(t)), normalize(t));
});

test('dividers carry the span a drag needs to work in fractions', () => {
  const t = splitGroup(singleGroup([1]), ROOT, 'row', 2);
  const { dividers } = layout(t, STAGE, { gutter: 4, strip: 24 });
  assert.equal(dividers[0].span, 996, 'the split span less its one gutter');
  // A 100px drag on a 996px span is ~10 points of fraction.
  const moved = resizeSplit(t, dividers[0].path, dividers[0].index, 100 / dividers[0].span) as Split;
  assert.ok(Math.abs(moved.sizes[0] - (0.5 + 100 / 996)) < 1e-9);
});

test('divider span accounts for every gutter in an n-ary split', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);
  t = splitGroup(t, '1', 'row', 3);
  const { dividers } = layout(t, STAGE, { gutter: 4, strip: 24 });
  assert.equal(dividers.length, 2);
  for (const d of dividers) assert.equal(d.span, 992, '1000 less two gutters');
});

test('minFractionFor keeps a dragged pane readable', () => {
  // A 1000px-wide row: the floor is the 140px minimum as a fraction.
  assert.ok(Math.abs(minFractionFor('row', 1000) - 0.14) < 1e-9);
  // A narrow split can't demand more than it has: capped, never absurd.
  assert.equal(minFractionFor('row', 200), 0.45);
  assert.ok(Math.abs(minFractionFor('col', 600) - (92 / 600)) < 1e-9);
});

test('equalizeAll rebalances nested splits, not just the top level', () => {
  let t: LayoutNode = singleGroup([1]);
  t = splitGroup(t, ROOT, 'row', 2);          // 0.5 / 0.5
  t = splitGroup(t, '1', 'col', 3);           // right becomes a column
  t = resizeSplit(t, ROOT, 0, 0.25);          // 0.75 / 0.25
  t = resizeSplit(t, '1', 0, 0.3);            // the nested column: 0.8 / 0.2
  const even = equalizeAll(t) as Split;
  assertInvariants(even, 'equalizeAll');
  assert.deepEqual(even.sizes, [0.5, 0.5]);
  const nested = even.children[1] as Split;
  assert.deepEqual(nested.sizes, [0.5, 0.5], 'the nested column evened too');
});

test('equalizeAll leaves an unsplit window alone', () => {
  const t = singleGroup([1, 2], 2);
  assert.deepEqual(equalizeAll(t), t);
});
