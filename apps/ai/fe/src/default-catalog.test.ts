// docs/AGENT_UX.md N5a/N5b — the launcher opens ready to go.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { defaultCatalog, defaultCwd } from './default-catalog.ts';

const all = [
  { id: 'anthropic', available: true },
  { id: 'openai', available: true },
  { id: 'openrouter', available: true },
];

test('the catalog you used last wins', () => {
  assert.equal(defaultCatalog(all, [{ catalog: 'openrouter' }]), 'openrouter');
});

test('a catalog that can no longer start does not win', () => {
  const noKey = [{ id: 'anthropic', available: true }, { id: 'openrouter', available: false }];
  assert.equal(defaultCatalog(noKey, [{ catalog: 'openrouter' }]), 'anthropic');
});

test('it walks back through history, past sessions started without a catalog', () => {
  assert.equal(defaultCatalog(all, [{}, { catalog: 'gone' }, { catalog: 'openai' }]), 'openai');
});

test('no history takes the first catalog that can start, in published order', () => {
  assert.equal(defaultCatalog([{ id: 'anthropic', available: false }, { id: 'openai', available: true }]), 'openai');
  assert.equal(defaultCatalog(all), 'anthropic');
});

test('nothing can start leaves the form unchosen', () => {
  assert.equal(defaultCatalog([{ id: 'anthropic', available: false }]), '');
  assert.equal(defaultCatalog([]), '');
});

test('the folder is the last one worked in, or empty for Home', () => {
  assert.equal(defaultCwd([{ catalog: 'anthropic', cwd: '/home/mick/wash' }]), '/home/mick/wash');
  // A history row with no directory is skipped, not rendered as blank.
  assert.equal(defaultCwd([{}, { cwd: '/srv' }]), '/srv');
  assert.equal(defaultCwd([]), '');
  assert.equal(defaultCwd(), '');
});
