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
import { clampViewport, viewportForRect, nextZ } from './viewport-math';

export type WinState = 'normal' | 'minimized' | 'maximized';

export interface CrashInfo {
  appID: string;
  exitCode: number;
  signal?: string;
  uptime: string;
  log: string;
}

export interface Win {
  windowID: number;
  instanceID: string;
  element: string;
  icon?: string;
  /** Brand accent inherited from the source app's manifest;
   * applied as `color:` on the titlebar icon. */
  accent?: string;
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
  // isRoot is router-attested; renders the red ROOT stripe in the
  // titlebar. See SessionWindow.is_root in main.tsx.
  isRoot?: boolean;
  // crashed is set when the BE process exited abnormally. The window
  // stays mounted (geometry intact) but FloatingWindow swaps in the
  // crash tombstone instead of the dead custom element. Router-side
  // window state is gone by this point — closing the tombstone is a
  // pure FE removal.
  crashed?: CrashInfo;
}

export interface DesktopMount {
  instanceID: string;
  element: string;
}

const [windows, setWindows] = createStore<Win[]>([]);
const [focused, setFocused] = createSignal<number | null>(null);
const [desktop, setDesktop] = createSignal<DesktopMount | null>(null);

// Virtual desktop is a VIEWPORTS_PER_AXIS² grid of viewports — windows
// live in one big plane (W*VIEWPORTS × H*VIEWPORTS in screen-pixel
// coords) and the shell pans a viewport-sized camera over it. Router-
// side window x/y is unaware of viewports; this is a pure shell-side
// projection so the router doesn't need to grow a "workspace" concept.
export const VIEWPORTS_PER_AXIS = 3;

const [screenSize, setScreenSize] = createSignal({
  w: window.innerWidth,
  h: window.innerHeight,
});
window.addEventListener('resize', () => {
  setScreenSize({ w: window.innerWidth, h: window.innerHeight });
});

// Viewport persists across reloads so the user comes back to whichever
// cell they were on. LocalStorage is per-origin and tab-shared; if a
// second tab on the same shell switches cells, this tab stays where it
// is (no cross-tab sync; that would require StorageEvent listening and
// isn't worth the complexity for v1).
const VIEWPORT_STORAGE_KEY = 'wash.viewport';
function loadStoredViewport(): { vx: number; vy: number } {
  try {
    const raw = localStorage.getItem(VIEWPORT_STORAGE_KEY);
    if (!raw) return { vx: 0, vy: 0 };
    const parsed = JSON.parse(raw) as { vx?: unknown; vy?: unknown };
    const max = VIEWPORTS_PER_AXIS - 1;
    const vx = typeof parsed.vx === 'number' ? Math.max(0, Math.min(max, Math.round(parsed.vx))) : 0;
    const vy = typeof parsed.vy === 'number' ? Math.max(0, Math.min(max, Math.round(parsed.vy))) : 0;
    return { vx, vy };
  } catch {
    return { vx: 0, vy: 0 };
  }
}
const [viewport, setViewportSignal] = createSignal(loadStoredViewport());

export { windows, focused, desktop, viewport, screenSize };

export function setViewport(vx: number, vy: number): void {
  const { vx: cvx, vy: cvy } = clampViewport(vx, vy, VIEWPORTS_PER_AXIS);
  const cur = viewport();
  if (cur.vx === cvx && cur.vy === cvy) return;
  setViewportSignal({ vx: cvx, vy: cvy });
  try {
    localStorage.setItem(VIEWPORT_STORAGE_KEY, JSON.stringify({ vx: cvx, vy: cvy }));
  } catch {
    // Storage quota / disabled — viewport just won't persist this run.
  }
}

// viewportFor maps a window to the viewport that "owns" its center.
// Used by the pager's window-click handler and the taskbar pill's
// dblclick handler to snap to the cell where a given window lives.
export function viewportFor(w: { x: number; y: number; w: number; h: number }): {
  vx: number;
  vy: number;
} {
  return viewportForRect(w, screenSize(), VIEWPORTS_PER_AXIS);
}

// raiseLocal bumps a window to the front locally; the router's patch
// confirms the change moments later. Kept as a separate export for
// places that already raise before calling a wire helper.
export function raiseLocal(windowID: number): void {
  setWindows((w) => w.windowID === windowID, 'z', nextZ(windows));
  setFocused(windowID);
}

// moveLocal / resizeLocal write x/y/w/h to the store immediately so
// the FloatingWindow can clear its drag-or-resize override without
// snapping back to the pre-commit position while waiting for the
// router's session.patch. The router's patch is canonical and will
// overwrite these moments later — usually with the same values.
export function moveLocal(windowID: number, x: number, y: number): void {
  setWindows((w) => w.windowID === windowID, { x, y });
}

export function resizeLocal(windowID: number, w: number, h: number): void {
  setWindows((win) => win.windowID === windowID, { w, h });
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
    icon: sw.icon,
    accent: sw.accent,
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
    isRoot: sw.is_root,
  };
}

// mountWhenReady upserts a window once its element is available. Built-in
// elements — custom elements the shell registers itself at startup, e.g.
// <wash-app-display> for wash-display's video surfaces — ship no app
// bundle, so they're already defined and we mount immediately. Bundle-
// backed app windows wait for their bundle to arrive (so the element
// definition exists before we instantiate it). Without the built-in
// fast-path, a wash-display window waited 10s for a bundle that never
// comes ("bundle for <id> not announced") and never mounted.
function mountWhenReady(
  w: Win,
  waitForBundle: (instanceID: string) => Promise<void>,
  where: string,
): void {
  if (typeof customElements !== 'undefined' && customElements.get(w.element)) {
    upsertWindow(w);
    return;
  }
  waitForBundle(w.instanceID)
    .then(() => upsertWindow(w))
    .catch((err) => console.error(`wash: ${where} window mount:`, err));
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
    mountWhenReady(w, waitForBundle, 'snapshot');
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
        // First sight of this window — mount when its element is ready
        // (immediately for built-ins, else after its bundle lands).
        mountWhenReady(w, waitForBundle, 'patch');
      }
      if (w.state === 'minimized' && focused() === w.windowID) {
        setFocused(null);
      } else if (p.window.focused) {
        setFocused(w.windowID);
      }
    } else if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      const id = p.window_id;
      // Crashed windows survive the router-side delete patch — they
      // become FE-only tombstones the user dismisses by clicking
      // close. The router has already torn down its side; we just
      // hold the geometry + crash info until the user is done.
      const w = windows.find((x) => x.windowID === id);
      if (w?.crashed) continue;
      setWindows((prev) => prev.filter((w) => w.windowID !== id));
      if (focused() === id) setFocused(null);
    }
  }
}

// markCrashed flips the tombstone bit on the matching window.
// Called from the shell's app.crashed handler in main.tsx. Idempotent.
// If the window isn't currently in the store (rare race: the BE
// crashed before its window-upsert reached the shell), this is a
// no-op — there's nothing to tombstone.
export function markCrashed(instanceID: string, info: CrashInfo): void {
  const idx = windows.findIndex((w) => w.instanceID === instanceID);
  if (idx < 0) return;
  setWindows(idx, 'crashed', info);
}

// dismissCrashed removes a crash tombstone from the WM store. The
// close button on a crashed window calls this directly rather than
// sending window.close_clicked — the router-side state is already
// gone.
export function dismissCrashed(windowID: number): void {
  setWindows((prev) => prev.filter((w) => w.windowID !== windowID));
  if (focused() === windowID) setFocused(null);
}

function upsertWindow(w: Win): void {
  const idx = windows.findIndex((x) => x.windowID === w.windowID);
  if (idx < 0) {
    setWindows((prev) => [...prev, w]);
  } else {
    setWindows(idx, w);
  }
}
