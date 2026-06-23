// Desktop surface: mounts the session app's element as the root
// background. For v0.0, the element fills the viewport; floating
// windows are absolutely positioned on top.

import { createEffect } from 'solid-js';
import { tokens } from '@wash/ui';
import { registerMountedElement, unregisterMountedElement } from './api';
import { desktop } from './wm';

export function Desktop() {
  let host!: HTMLDivElement;
  let mountedFor: string | null = null;

  createEffect(() => {
    const d = desktop();
    if (!d) {
      if (mountedFor) {
        unregisterMountedElement(mountedFor);
      }
      host.replaceChildren();
      mountedFor = null;
      return;
    }
    if (mountedFor === d.instanceID) return;
    if (mountedFor) {
      unregisterMountedElement(mountedFor);
    }
    host.replaceChildren();
    const el = document.createElement(d.element);
    el.setAttribute('data-wash-instance', d.instanceID);
    host.appendChild(el);
    // See window.tsx for the rationale: defer one microtask so
    // mounted-app listeners are set up before wash:state /
    // wash:msg dispatch.
    queueMicrotask(() => registerMountedElement(d.instanceID, el));
    mountedFor = d.instanceID;
  });

  return (
    <div
      ref={host}
      class="wash-desktop"
      style={{
        // position:absolute (not fixed) — `position:fixed` always
        // creates a stacking context, which would trap the session
        // app's taskbar (z-index:10000) beneath floating windows.
        // #root is already position:fixed full-viewport so absolute
        // inset:0 here covers the same area without the context.
        position: 'absolute',
        inset: '0',
        // Bare fallback behind the session app's wallpaper; tokenized so a
        // light pack doesn't flash near-black before the wallpaper paints.
        background: tokens.bgWindow,
      }}
    />
  );
}
