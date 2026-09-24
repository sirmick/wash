import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { agentproto } from '@wash/ui';
import { applyUsagePatch } from './usage-patch.ts';

test('usage patches update only named rows with the latest counters', () => {
  const a = { key: 'acp:1', used: 1, size: 10 } as agentproto.Row;
  const b = { key: 'acp:2', used: 2, size: 20 } as agentproto.Row;
  const state = { rows: [a, b], recent: ['kept'] };

  const next = applyUsagePatch(state, {
    kind: 'usage_patch',
    rows: [
      { key: 'acp:2', used: 9, size: 99 },
      { key: 'gone', used: 100, size: 100 },
    ],
  });

  assert.deepEqual(next.rows, [a, { ...b, used: 9, size: 99 }]);
  assert.deepEqual(next.recent, ['kept']);
});
