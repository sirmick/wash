import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { focusFromSnapshot } from './wm-focus.ts';

test('focusFromSnapshot returns null for an empty snapshot', () => {
  assert.equal(focusFromSnapshot([]), null);
});

test('focusFromSnapshot returns null when no window claims focus', () => {
  assert.equal(
    focusFromSnapshot([
      { window_id: 1, focused: false },
      { window_id: 2 }, // focused undefined
    ]),
    null,
  );
});

test('focusFromSnapshot returns the id of the single focused window', () => {
  assert.equal(
    focusFromSnapshot([
      { window_id: 1, focused: false },
      { window_id: 2, focused: true },
      { window_id: 3, focused: false },
    ]),
    2,
  );
});

test('focusFromSnapshot picks the focused window regardless of position', () => {
  assert.equal(focusFromSnapshot([{ window_id: 7, focused: true }]), 7);
  assert.equal(
    focusFromSnapshot([
      { window_id: 9, focused: true },
      { window_id: 4, focused: false },
    ]),
    9,
  );
});

test('focusFromSnapshot: last claim wins if more than one is marked', () => {
  // The router attests a single focus, but if two were marked the last in
  // iteration order wins — preserving the old in-loop overwrite semantics.
  assert.equal(
    focusFromSnapshot([
      { window_id: 1, focused: true },
      { window_id: 2, focused: true },
    ]),
    2,
  );
});
