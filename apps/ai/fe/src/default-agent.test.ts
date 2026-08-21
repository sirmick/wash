// docs/AGENT_UX.md N5a — the launcher opens ready to go.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { defaultAgent } from './default-agent.ts';

test('prefers claude when installed, regardless of table order', () => {
  assert.equal(
    defaultAgent([
      { id: 'codex', available: true },
      { id: 'claude', available: true },
      { id: 'gemini', available: true },
    ]),
    'claude',
  );
});

test('an uninstalled claude is not preferred over an installed adapter', () => {
  assert.equal(
    defaultAgent([
      { id: 'codex', available: true },
      { id: 'claude', available: false },
    ]),
    'codex',
  );
});

test('falls back to the first available in published order', () => {
  assert.equal(
    defaultAgent([
      { id: 'codex', available: false },
      { id: 'gemini', available: true },
    ]),
    'gemini',
  );
});

test('nothing installed leaves the form on Choose…', () => {
  assert.equal(defaultAgent([{ id: 'claude', available: false }]), '');
  assert.equal(defaultAgent([]), '');
});
