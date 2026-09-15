import { test } from 'node:test';
import assert from 'node:assert/strict';
import { isManagerElement } from './role.ts';

test('the manager is its tag on any host; a controller never is', () => {
  assert.equal(isManagerElement('WASH-APP-AGENTS'), true);
  // A remote manager mounts under an origin-suffixed tag. Missing this left
  // B's manager rendering as an unattached session window after a reload.
  assert.equal(isManagerElement('wash-app-agents-remoteb'), true);
  assert.equal(isManagerElement('wash-app-ai'), false);
  assert.equal(isManagerElement('wash-app-ai-remoteb'), false);
});
