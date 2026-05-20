// Window manager state — a flat list of floating windows plus the
// (optional) desktop-surface instance. Solid signals drive the view
// in main.tsx.
//
// v0.0 only covers: drag, focus-raise, close-click. Resize, min/max,
// taskbar, etc. are deferred.

import { createSignal } from 'solid-js';
import { createStore } from 'solid-js/store';

export type WinState = 'normal' | 'minimized' | 'maximized';

export interface Win {
  windowID: number;
  instanceID: string;
  element: string; // tag name, e.g. wash-app-about
  title: string;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
  state: WinState;
  // Pre-min/max geometry so restore returns to the user-set frame.
  restoreX?: number;
  restoreY?: number;
  restoreW?: number;
  restoreH?: number;
}

export interface DesktopMount {
  instanceID: string;
  element: string;
}

const [windows, setWindows] = createStore<Win[]>([]);
const [focused, setFocused] = createSignal<number | null>(null);
const [desktop, setDesktop] = createSignal<DesktopMount | null>(null);
let nextZ = 1;

export { windows, focused, desktop };

let nextOffset = 0;

export function addWindow(
  w: Omit<Win, 'x' | 'y' | 'z' | 'state'> & { x?: number; y?: number },
): Win {
  const x = w.x ?? 40 + nextOffset;
  const y = w.y ?? 40 + nextOffset;
  nextOffset = (nextOffset + 24) % 200;
  const win: Win = { ...w, x, y, z: ++nextZ, state: 'normal' };
  setWindows((prev) => [...prev, win]);
  setFocused(win.windowID);
  return win;
}

export function removeWindow(windowID: number): void {
  setWindows((prev) => prev.filter((w) => w.windowID !== windowID));
  if (focused() === windowID) setFocused(null);
}

export function setTitle(windowID: number, title: string): void {
  setWindows((w) => w.windowID === windowID, 'title', title);
}

export function raise(windowID: number): void {
  setWindows((w) => w.windowID === windowID, 'z', ++nextZ);
  setFocused(windowID);
}

export function move(windowID: number, x: number, y: number): void {
  setWindows((w) => w.windowID === windowID, { x, y });
}

export function resize(windowID: number, w: number, h: number): void {
  setWindows((win) => win.windowID === windowID, { w, h });
}

// Saving geometry before min/max happens inside these setters so a
// double-min or chained max → max keeps the original normal frame.
function saveRestoreFrom(w: Win): Partial<Win> {
  return { restoreX: w.x, restoreY: w.y, restoreW: w.w, restoreH: w.h };
}

export function minimize(windowID: number): void {
  setWindows(
    (w) => w.windowID === windowID && w.state !== 'minimized',
    (w) =>
      w.state === 'normal'
        ? { ...w, ...saveRestoreFrom(w), state: 'minimized' }
        : { ...w, state: 'minimized' },
  );
  if (focused() === windowID) setFocused(null);
}

export function maximize(windowID: number): void {
  setWindows(
    (w) => w.windowID === windowID && w.state !== 'maximized',
    (w) =>
      w.state === 'normal'
        ? { ...w, ...saveRestoreFrom(w), state: 'maximized' }
        : { ...w, state: 'maximized' },
  );
}

// restoreWin returns a window to "normal" with its pre-min/max frame.
export function restoreWin(windowID: number): void {
  setWindows(
    (w) => w.windowID === windowID && w.state !== 'normal',
    (w) => ({
      ...w,
      state: 'normal',
      x: w.restoreX ?? w.x,
      y: w.restoreY ?? w.y,
      w: w.restoreW ?? w.w,
      h: w.restoreH ?? w.h,
    }),
  );
}

export function mountDesktop(d: DesktopMount): void {
  setDesktop(d);
}

export function unmountDesktop(): void {
  setDesktop(null);
}
