import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { serviceBadge, isActive, isFailed } from './service-status.ts';

test('failed wins over everything (active=failed or sub=crashed)', () => {
  assert.deepEqual(serviceBadge('failed', 'dead'), { tone: 'failed', label: 'failed' });
  assert.deepEqual(serviceBadge('active', 'crashed'), { tone: 'failed', label: 'failed' });
});

test('active/reloading → active tone, label is the sub-state (or "running")', () => {
  assert.deepEqual(serviceBadge('active', 'running'), { tone: 'active', label: 'running' });
  assert.deepEqual(serviceBadge('active', ''), { tone: 'active', label: 'running' });
  assert.deepEqual(serviceBadge('reloading', 'reload'), { tone: 'active', label: 'reload' });
});

test('activating/deactivating → transitioning tone, label is the active-state', () => {
  assert.deepEqual(serviceBadge('activating', 'start'), { tone: 'transitioning', label: 'activating' });
  assert.deepEqual(serviceBadge('deactivating', 'stop'), { tone: 'transitioning', label: 'deactivating' });
});

test('everything else → inactive tone, label falls back sub → active → "inactive"', () => {
  assert.deepEqual(serviceBadge('inactive', 'dead'), { tone: 'inactive', label: 'dead' });
  assert.deepEqual(serviceBadge('inactive', ''), { tone: 'inactive', label: 'inactive' });
  assert.deepEqual(serviceBadge('', ''), { tone: 'inactive', label: 'inactive' });
  assert.deepEqual(serviceBadge('unknown', ''), { tone: 'inactive', label: 'unknown' });
});

test('isActive: only active/reloading', () => {
  assert.equal(isActive('active'), true);
  assert.equal(isActive('reloading'), true);
  assert.equal(isActive('inactive'), false);
  assert.equal(isActive('activating'), false);
});

test('isFailed: active=failed or sub=crashed', () => {
  assert.equal(isFailed('failed', ''), true);
  assert.equal(isFailed('active', 'crashed'), true);
  assert.equal(isFailed('active', 'running'), false);
});
