// Unit tests for the start menu's named tune (tune.ts).
//
// Run: node --test --conditions=browser apps/radio/fe/src/tune.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import { decideTune } from './tune.ts';

test('decideTune: a listed station plays by its BE index', () => {
  assert.deepEqual(decideTune('Drone Zone', ['Groove Salad', 'Drone Zone'], true), { kind: 'play', be: 1 });
});

test('decideTune: no list (or no stream base) yet holds the name for the list', () => {
  assert.deepEqual(decideTune('Drone Zone', [], true), { kind: 'hold' });
  assert.deepEqual(decideTune('Drone Zone', ['Drone Zone'], false), { kind: 'hold' });
});

test('decideTune: a list without the station drops it rather than waiting for a later one', () => {
  assert.deepEqual(decideTune('Pasted FM', ['Groove Salad'], true), { kind: 'drop' });
  assert.deepEqual(decideTune('', ['Groove Salad'], true), { kind: 'drop' });
});
