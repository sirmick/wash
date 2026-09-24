// docs/AGENT_UX.md N5a/N5b — the launcher opens ready to go.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { defaultStack, defaultCwd } from './default-stack.ts';

const all = [
  { id: 'anthropic', available: true },
  { id: 'openai', available: true },
  { id: 'openrouter', available: true },
];

test('the stack you used last wins', () => {
  assert.equal(defaultStack(all, [{ stack: 'openrouter' }]), 'openrouter');
});

test('a stack that can no longer start does not win', () => {
  const noKey = [{ id: 'anthropic', available: true }, { id: 'openrouter', available: false }];
  assert.equal(defaultStack(noKey, [{ stack: 'openrouter' }]), 'anthropic');
});

test('it walks back through history, past sessions started without a stack', () => {
  assert.equal(defaultStack(all, [{}, { stack: 'gone' }, { stack: 'openai' }]), 'openai');
});

test('no history takes the first stack that can start, in published order', () => {
  assert.equal(defaultStack([{ id: 'anthropic', available: false }, { id: 'openai', available: true }]), 'openai');
  assert.equal(defaultStack(all), 'anthropic');
});

test('nothing can start leaves the form unchosen', () => {
  assert.equal(defaultStack([{ id: 'anthropic', available: false }]), '');
  assert.equal(defaultStack([]), '');
});

test('the folder is the last one worked in, or empty for Home', () => {
  assert.equal(defaultCwd([{ stack: 'anthropic', cwd: '/home/mick/wash' }]), '/home/mick/wash');
  // A history row with no directory is skipped, not rendered as blank.
  assert.equal(defaultCwd([{}, { cwd: '/srv' }]), '/srv');
  assert.equal(defaultCwd([]), '');
  assert.equal(defaultCwd(), '');
});
