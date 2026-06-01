import { test } from 'node:test';
import { strict as assert } from 'node:assert';
import { setAtPath } from './setAtPath.ts';

test('sets a top-level key', () => {
  assert.deepEqual(setAtPath({}, ['Proto'], { _tag: 'dhcp' }), { Proto: { _tag: 'dhcp' } });
});

test('creates missing intermediate objects for a nested path', () => {
  assert.deepEqual(setAtPath({}, ['Proto', 'IPAddr'], '10.0.0.1/24'), {
    Proto: { IPAddr: '10.0.0.1/24' },
  });
});

test('preserves sibling keys at every level', () => {
  const before = { Name: 'lan', Proto: { _tag: 'static', IPAddr: 'a', Gateway: 'g' } };
  const after = setAtPath(before, ['Proto', 'IPAddr'], 'b');
  assert.deepEqual(after, { Name: 'lan', Proto: { _tag: 'static', IPAddr: 'b', Gateway: 'g' } });
});

test('union switch (bare discriminator) replaces the whole variant, dropping its fields', () => {
  const before = { Proto: { _tag: 'static', IPAddr: '1.2.3.4/24', Gateway: '1.2.3.1' } };
  // Setting the bare "Proto" path to {_tag} must NOT merge — the old static
  // fields are gone, matching how a method-select change behaves.
  assert.deepEqual(setAtPath(before, ['Proto'], { _tag: 'dhcp' }), { Proto: { _tag: 'dhcp' } });
});

test('is immutable — the input and its nested objects are untouched', () => {
  const before = { Proto: { _tag: 'static', IPAddr: 'a' } };
  const proto = before.Proto;
  const after = setAtPath(before, ['Proto', 'IPAddr'], 'b');
  assert.equal(before.Proto, proto, 'original nested object reference unchanged');
  assert.equal(before.Proto.IPAddr, 'a', 'original value unchanged');
  assert.notEqual(after, before);
  assert.notEqual(after.Proto, before.Proto, 'a fresh object is produced along the written path');
});

test('a deep three-level path threads through, creating intermediates', () => {
  assert.deepEqual(setAtPath({ a: { keep: 1 } }, ['a', 'b', 'c'], 9), {
    a: { keep: 1, b: { c: 9 } },
  });
});
