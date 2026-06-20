// Unit tests for VirtualGrid's windowing math (computeVirtualWindow).
// Run via `make fe-unit` (node --test --conditions=browser).

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { computeVirtualWindow, type VirtualWindowInput } from './virtual-window.ts';

const base: VirtualWindowInput = {
  count: 100,
  tileWidth: 100,
  tileHeight: 100,
  gap: 10,
  overscan: 2,
  vw: 0,
  vh: 0,
  scrollTop: 0,
};

test('column count from content width', () => {
  // (vw + gap) / (tileWidth + gap): (450+10)/(100+10) = 4.18 -> 4 cols
  assert.equal(computeVirtualWindow({ ...base, vw: 450 }).cols, 4);
  // exact fit for 5: 5*110 - 10 = 540 width -> (540+10)/110 = 5
  assert.equal(computeVirtualWindow({ ...base, vw: 540 }).cols, 5);
});

test('cols is at least 1 even with zero/with tiny width (pre-measure)', () => {
  assert.equal(computeVirtualWindow({ ...base, vw: 0 }).cols, 1);
  assert.equal(computeVirtualWindow({ ...base, vw: 30 }).cols, 1);
});

test('row pitch and total spacer height (no trailing gap)', () => {
  // 100 items, 4 cols -> 25 rows; rowH = 110; totalH = 25*110 - 10 = 2740
  const w = computeVirtualWindow({ ...base, vw: 450 });
  assert.equal(w.rowH, 110);
  assert.equal(w.totalRows, 25);
  assert.equal(w.totalH, 2740);
});

test('empty collection: zero rows, zero height', () => {
  const w = computeVirtualWindow({ ...base, count: 0, vw: 450, vh: 400 });
  assert.equal(w.totalRows, 0);
  assert.equal(w.totalH, 0);
  assert.equal(w.startIndex, 0);
  assert.equal(w.endIndex, 0);
});

test('scrolled-to-top window includes overscan below the viewport', () => {
  // vh=300 -> ceil(300/110)=3 visible rows; +2 overscan = endRow 5; startRow 0.
  const w = computeVirtualWindow({ ...base, vw: 450, vh: 300, scrollTop: 0 });
  assert.equal(w.startRow, 0);
  assert.equal(w.endRow, 5);
  assert.equal(w.startIndex, 0);
  assert.equal(w.endIndex, 20); // 5 rows * 4 cols
});

test('scrolled into the middle clamps startRow with overscan', () => {
  // scrollTop 1100 -> floor(1100/110)=10; -2 overscan = startRow 8.
  // endRow = ceil((1100+300)/110)=13; +2 = 15.
  const w = computeVirtualWindow({ ...base, vw: 450, vh: 300, scrollTop: 1100 });
  assert.equal(w.startRow, 8);
  assert.equal(w.endRow, 15);
  assert.equal(w.startIndex, 32); // 8*4
  assert.equal(w.endIndex, 60); // 15*4
});

test('endRow never exceeds totalRows at the bottom', () => {
  // Near the end: scrollTop deep, endRow clamps to 25 (totalRows).
  const w = computeVirtualWindow({ ...base, vw: 450, vh: 300, scrollTop: 2600 });
  assert.equal(w.totalRows, 25);
  assert.ok(w.endRow <= 25, `endRow ${w.endRow} should clamp to totalRows`);
  assert.equal(w.endIndex, 100); // never slices past the array length intent
});

test('single-column (list) layout, e.g. the image viewer strip', () => {
  // tileWidth wide enough that only 1 col fits.
  const w = computeVirtualWindow({ ...base, count: 50, tileWidth: 170, vw: 180, vh: 420, scrollTop: 0, tileHeight: 84, gap: 2 });
  assert.equal(w.cols, 1);
  assert.equal(w.rowH, 86);
  // ceil(420/86)=5 rows + 2 overscan = 7 items from the top
  assert.equal(w.endIndex, 7);
});
