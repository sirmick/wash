// A connection blip remounts nothing, so the shell tells the windows that
// survived it (GH #21): createAppBus's onReconnect is how an app hears.
//
// @vitest-environment jsdom

import { describe, it, expect } from 'vitest';
import { createRoot } from 'solid-js';
import { createAppBus } from '@wash/ui';
import { announceReconnect, registerMountedElement, unregisterMountedElement } from './api.ts';
import { compoundInstanceId } from './clients.ts';

function mounted(id: string) {
  const host = document.createElement('div');
  let calls = 0;
  const dispose = createRoot((d) => {
    createAppBus({ instance: id, host }, { onReconnect: () => { calls += 1; } });
    return d;
  });
  registerMountedElement(id, host);
  return { calls: () => calls, dispose: () => { dispose(); unregisterMountedElement(id); } };
}

describe('announceReconnect', () => {
  it('reaches every mounted element of the origin that reconnected, and only those', () => {
    const local = mounted('i-agents');
    const remote = mounted(compoundInstanceId('build01', 'i-agents'));
    announceReconnect('local');
    expect(local.calls()).toBe(1);
    expect(remote.calls()).toBe(0);
    announceReconnect('build01');
    expect(remote.calls()).toBe(1);
    expect(local.calls()).toBe(1);
    local.dispose();
    remote.dispose();
  });

  it('says nothing to an element that has unmounted', () => {
    const w = mounted('i-gone');
    w.dispose();
    announceReconnect('local');
    expect(w.calls()).toBe(0);
  });
});
