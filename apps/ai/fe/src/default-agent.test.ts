// docs/AGENT_UX.md N5a — the launcher opens ready to go.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import { defaultAgent, defaultCwd } from './default-agent.ts';

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

// --- N5b: what you used last, when it is still usable ---

const usable = [
  { id: 'codex', available: true },
  { id: 'claude', available: true },
  { id: 'gemini', available: true },
];

test('the agent you used last beats the static claude preference', () => {
  assert.equal(defaultAgent(usable, [{ agent: 'gemini' }]), 'gemini');
});

test('an agent uninstalled since you last used it does not win', () => {
  assert.equal(
    defaultAgent([{ id: 'codex', available: true }, { id: 'claude', available: true }], [
      { agent: 'gemini' },
    ]),
    'claude',
  );
});

test('it walks back through history until it finds one still installed', () => {
  assert.equal(
    defaultAgent([{ id: 'codex', available: true }], [{ agent: 'gemini' }, { agent: 'codex' }]),
    'codex',
  );
});

test('no history falls back to the claude preference', () => {
  assert.equal(defaultAgent(usable, []), 'claude');
  assert.equal(defaultAgent(usable), 'claude');
});

test('the folder is the last one worked in, or empty for Home', () => {
  assert.equal(defaultCwd([{ agent: 'claude', cwd: '/home/mick/wash' }]), '/home/mick/wash');
  // A history row with no directory is skipped, not rendered as blank.
  assert.equal(defaultCwd([{ agent: 'claude' }, { agent: 'codex', cwd: '/srv' }]), '/srv');
  assert.equal(defaultCwd([]), '');
  assert.equal(defaultCwd(), '');
});
