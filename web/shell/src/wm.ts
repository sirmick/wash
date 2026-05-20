// Window manager state — a flat list of floating windows plus the
// (optional) desktop-surface instance. Solid signals drive the view
// in main.tsx.
//
// v0.0 only covers: drag, focus-raise, close-click. Resize, min/max,
// taskbar, etc. are deferred.

import { createSignal } from 'solid-js';
import { createStore } from 'solid-js/store';

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

export function addWindow(w: Omit<Win, 'x' | 'y' | 'z'> & { x?: number; y?: number }): Win {
  const x = w.x ?? 40 + nextOffset;
  const y = w.y ?? 40 + nextOffset;
  nextOffset = (nextOffset + 24) % 200;
  const win: Win = { ...w, x, y, z: ++nextZ };
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

export function mountDesktop(d: DesktopMount): void {
  setDesktop(d);
}

export function unmountDesktop(): void {
  setDesktop(null);
}
