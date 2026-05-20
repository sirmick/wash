// Desktop surface: mounts the session app's element as the root
// background. For v0.0, the element fills the viewport; floating
// windows are absolutely positioned on top.

import { createEffect } from 'solid-js';
import { desktop } from './wm';

export function Desktop() {
  let host!: HTMLDivElement;
  let mountedFor: string | null = null;

  createEffect(() => {
    const d = desktop();
    if (!d) {
      // teardown if the desktop instance went away
      host.replaceChildren();
      mountedFor = null;
      return;
    }
    if (mountedFor === d.instanceID) return;
    host.replaceChildren();
    const el = document.createElement(d.element);
    host.appendChild(el);
    mountedFor = d.instanceID;
  });

  return (
    <div
      ref={host}
      class="wash-desktop"
      style={{
        position: 'fixed',
        inset: '0',
        background: '#111',
        'z-index': '0',
      }}
    />
  );
}
