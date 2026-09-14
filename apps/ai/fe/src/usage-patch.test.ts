import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { RosterRow } from '@wash/ui';
import { applyUsagePatch } from './usage-patch.ts';

test('usage patches update only named rows with the latest counters', () => {
  const a = { key: 'acp:1', used: 1, size: 10 } as RosterRow;
  const b = { key: 'acp:2', used: 2, size: 20 } as RosterRow;
  const state = { rows: [a, b], recent: ['kept'] };

  const next = applyUsagePatch(state, [
    { key: 'acp:2', used: 9, size: 99 },
    { key: 'gone', used: 100, size: 100 },
  ]);

  assert.deepEqual(next.rows, [a, { ...b, used: 9, size: 99 }]);
  assert.deepEqual(next.recent, ['kept']);
});
