// Tests for the multi-router client registry + origin/compound-id codec.
// The codec is pure; the registry is exercised with a stub cast as a
// RouterClient (no Conn / WebSocket constructed).
//
// Run with: cd web/shell && npx tsx --test src/clients.test.ts

import { test } from 'node:test';
import { strict as assert } from 'node:assert';

import {
  LOCAL_ORIGIN,
  compoundInstanceId,
  parseInstanceId,
  registerClient,
  unregisterClient,
  clientForOrigin,
  clientForInstance,
  origins,
} from './clients.ts';
import type { RouterClient } from './router-client.ts';

test('local instance ids are unprefixed — single-host path is byte-for-byte unchanged', () => {
  assert.equal(compoundInstanceId(LOCAL_ORIGIN, 'i-3'), 'i-3');
  const p = parseInstanceId('i-3');
  assert.equal(p.origin, LOCAL_ORIGIN);
  assert.equal(p.bare, 'i-3');
});

test('remote instance ids round-trip through the origin tag', () => {
  const c = compoundInstanceId('hostB', 'i-3');
  assert.notEqual(c, 'i-3', 'remote id must be tagged');
  const p = parseInstanceId(c);
  assert.equal(p.origin, 'hostB');
  assert.equal(p.bare, 'i-3');
});

test('a bare id with no separator parses as local', () => {
  assert.deepEqual(parseInstanceId('whatever-i-7'), { origin: LOCAL_ORIGIN, bare: 'whatever-i-7' });
});

test('registry resolves clients by origin and by compound instance id', () => {
  const fake = { mark: 'B' } as unknown as RouterClient;
  registerClient('hostB', fake);
  assert.equal(clientForOrigin('hostB'), fake);
  assert.equal(clientForInstance(compoundInstanceId('hostB', 'i-9')), fake);
  assert.ok(origins().includes('hostB'));

  unregisterClient('hostB');
  assert.equal(clientForOrigin('hostB'), undefined);
  assert.equal(clientForInstance(compoundInstanceId('hostB', 'i-9')), undefined);
});

test('clientForInstance on a bare (local) id resolves the local client', () => {
  const localFake = { mark: 'local' } as unknown as RouterClient;
  registerClient(LOCAL_ORIGIN, localFake);
  assert.equal(clientForInstance('i-1'), localFake);
  unregisterClient(LOCAL_ORIGIN);
});
