// Window manager state — a pure projection of the router's
// session state. The shell receives session.snapshot on connect
// and session.patch on every change; applyPatch/applySnapshot
// drive the Solid store, and components render from it.
//
// No mutations live here. Locally-originated changes (drag, focus,
// state transitions) go via the wire as window.move / window.focus /
// window.state / window.resize messages; the router applies them and
// broadcasts the resulting patch back, which lands in the store.

import { createSignal } from 'solid-js';
import { createStore } from 'solid-js/store';
import type { SessionPatch, SessionWindow } from './main';

export type WinState = 'normal' | 'minimized' | 'maximized';

export interface Win {
  windowID: number;
  instanceID: string;
  element: string;
  title: string;
  x: number;
  y: number;
  w: number;
  h: number;
  z: number;
  state: WinState;
  // Pre-min/max geometry — kept around so a future "restore" call can
  // return the window to its user-set frame even after a chain of
  // min → max → restore. Tracked router-side; shells just mirror.
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

export { windows, focused, desktop };

// raiseLocal bumps a window to the front locally; the router's patch
// confirms the change moments later. Kept as a separate export for
// places that already raise before calling a wire helper.
export function raiseLocal(windowID: number): void {
  let maxZ = 0;
  for (const w of windows) {
    if (w.z > maxZ) maxZ = w.z;
  }
  setWindows((w) => w.windowID === windowID, 'z', maxZ + 1);
  setFocused(windowID);
}

export function mountDesktop(d: DesktopMount): void {
  setDesktop(d);
}

export function unmountDesktop(): void {
  setDesktop(null);
}

// fromSessionWindow projects the wire shape onto the local Win.
function fromSessionWindow(sw: SessionWindow): Win {
  return {
    windowID: sw.window_id,
    instanceID: sw.instance_id,
    element: sw.element,
    title: sw.title,
    x: sw.x,
    y: sw.y,
    w: sw.w,
    h: sw.h,
    z: sw.z,
    state: sw.state,
    restoreX: sw.restore_x,
    restoreY: sw.restore_y,
    restoreW: sw.restore_w,
    restoreH: sw.restore_h,
  };
}

// applySessionSnapshot replaces the store with the router's full
// state. Each new window waits for its bundle to be ready before
// landing in the store so onMount can resolve the custom element.
//
// Existing windows whose ids are absent in the snapshot are removed —
// this is the reconnect path; the snapshot is authoritative.
export function applySessionSnapshot(
  sessionWins: SessionWindow[],
  waitForBundle: (instanceID: string) => Promise<void>,
): void {
  const keep = new Set<number>();
  for (const sw of sessionWins) {
    keep.add(sw.window_id);
  }
  // Drop windows that aren't in the new snapshot.
  setWindows((prev) => prev.filter((w) => keep.has(w.windowID)));

  for (const sw of sessionWins) {
    const w = fromSessionWindow(sw);
    waitForBundle(w.instanceID)
      .then(() => upsertWindow(w))
      .catch((err) => console.error('wash: snapshot window mount:', err));
    if (sw.focused) setFocused(sw.window_id);
  }
  if (sessionWins.length === 0 || !sessionWins.some((w) => w.focused)) {
    // No focus claim in the snapshot → clear local focus.
    if (!sessionWins.some((w) => w.focused)) setFocused(null);
  }
}

// applySessionPatch applies a batch of mutations in order. Upserts
// wait for the bundle if the instance hasn't been declared yet.
export function applySessionPatch(
  patches: SessionPatch[],
  waitForBundle: (instanceID: string) => Promise<void>,
): void {
  for (const p of patches) {
    if (p.op === 'window.upsert' && p.window) {
      const w = fromSessionWindow(p.window);
      const existed = windows.find((x) => x.windowID === w.windowID) != null;
      if (existed) {
        upsertWindow(w);
      } else {
        // First sight of this window — wait for the bundle to land.
        waitForBundle(w.instanceID)
          .then(() => upsertWindow(w))
          .catch((err) => console.error('wash: patch window mount:', err));
      }
      if (w.state === 'minimized' && focused() === w.windowID) {
        setFocused(null);
      } else if (p.window.focused) {
        setFocused(w.windowID);
      }
    } else if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      const id = p.window_id;
      setWindows((prev) => prev.filter((w) => w.windowID !== id));
      if (focused() === id) setFocused(null);
    }
  }
}

function upsertWindow(w: Win): void {
  const idx = windows.findIndex((x) => x.windowID === w.windowID);
  if (idx < 0) {
    setWindows((prev) => [...prev, w]);
  } else {
    setWindows(idx, w);
  }
}
