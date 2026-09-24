// Unit tests for the window-switcher / show-desktop decisions (switcher.ts).
// Run: node --test --conditions=browser web/shell/src/switcher.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  chordReleased,
  cycle,
  initialIndex,
  isShowDesktopChord,
  isSwitcherChord,
  mruOrder,
  showDesktopPlan,
  type SwitchableWin,
} from './switcher.ts';

const w = (id: number, gz: number, state: SwitchableWin['state'] = 'normal', origin = 'local'): SwitchableWin => ({
  origin,
  windowID: id,
  gz,
  state,
});

test('mruOrder: highest gz (most recently focused) first, minimised included, stable ties', () => {
  const out = mruOrder([w(1, 3), w(2, 9, 'minimized'), w(3, 5), w(4, 5, 'normal', 'remote')]);
  assert.deepEqual(out.map((x) => x.windowID), [2, 3, 4, 1]);
});

test('initialIndex highlights the previous window when there is one', () => {
  assert.equal(initialIndex(0), 0);
  assert.equal(initialIndex(1), 0);
  assert.equal(initialIndex(3), 1);
});

test('cycle wraps both ways', () => {
  assert.equal(cycle(1, 3, false), 2);
  assert.equal(cycle(2, 3, false), 0);
  assert.equal(cycle(0, 3, true), 2);
  assert.equal(cycle(0, 0, false), 0);
});

test('chords: Ctrl+Alt+Tab (any shift) is the switcher, Ctrl+Alt+D show-desktop, Meta never', () => {
  const k = (key: string, o: Partial<{ ctrlKey: boolean; altKey: boolean; metaKey: boolean; shiftKey: boolean }> = {}) => ({
    key,
    ctrlKey: false,
    altKey: false,
    metaKey: false,
    shiftKey: false,
    ...o,
  });
  assert.ok(isSwitcherChord(k('Tab', { ctrlKey: true, altKey: true })));
  assert.ok(isSwitcherChord(k('Tab', { ctrlKey: true, altKey: true, shiftKey: true })));
  assert.ok(!isSwitcherChord(k('Tab', { ctrlKey: true })));
  assert.ok(!isSwitcherChord(k('Tab', { altKey: true })));
  assert.ok(!isSwitcherChord(k('Tab', { ctrlKey: true, altKey: true, metaKey: true })));
  assert.ok(isShowDesktopChord(k('d', { ctrlKey: true, altKey: true })));
  assert.ok(isShowDesktopChord(k('D', { ctrlKey: true, altKey: true })));
  assert.ok(!isShowDesktopChord(k('d', { ctrlKey: true, altKey: true, shiftKey: true })));
  assert.ok(!isShowDesktopChord(k('d', { ctrlKey: true })));
  assert.ok(chordReleased('Control'));
  assert.ok(chordReleased('Alt'));
  assert.ok(!chordReleased('Tab'));
  assert.ok(!chordReleased('Shift'));
});

test('showDesktopPlan: minimise what is showing, remember it, restore it next time', () => {
  const wins = [w(1, 1), w(2, 2, 'minimized'), w(3, 3, 'maximized')];
  const p1 = showDesktopPlan(wins, null);
  assert.equal(p1.action, 'minimize');
  assert.deepEqual(p1.action === 'minimize' && p1.targets.map((t) => t.windowID), [1, 3]);

  // All hidden now; window 3 was closed meanwhile.
  const hidden = [w(1, 1, 'minimized'), w(2, 2, 'minimized')];
  const p2 = showDesktopPlan(hidden, p1.action === 'minimize' ? p1.targets : null);
  assert.equal(p2.action, 'restore');
  assert.deepEqual(p2.action === 'restore' && p2.targets.map((t) => t.windowID), [1]);

  // Nothing showing and nothing remembered: no-op (the user minimised by hand).
  assert.deepEqual(showDesktopPlan(hidden, null), { action: 'none' });
  assert.deepEqual(showDesktopPlan([], []), { action: 'none' });
});
