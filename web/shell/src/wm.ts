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
import { type Origin, LOCAL_ORIGIN } from './clients';
import { clampViewport, viewportForRect, nextZ } from './viewport-math';
import { focusFromSnapshot } from './wm-focus';

export type WinState = 'normal' | 'minimized' | 'maximized';

export interface CrashInfo {
  appID: string;
  exitCode: number;
  signal?: string;
  uptime: string;
  log: string;
}

export interface Win {
  // Which router this window belongs to. Window/instance ids are scoped
  // to a connection, so identity in the merged store is (origin,windowID)
  // — never windowID alone. LOCAL for the shell's own router; a remote
  // host's origin for tunnelled windows (docs/REMOTE.md R2).
  origin: Origin;
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
  // chromeless drops the wash titlebar + border so the guest surface
  // (e.g. Webamp) fills the frame and draws its own chrome. Mirrors
  // the app manifest's WindowHints.Chromeless.
  chromeless?: boolean;
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

// A focus reference is (origin, windowID): at most one window is focused
// across the whole merged desktop, but its identity must name the origin
// so the same windowID on two routers isn't confused.
export interface FocusRef {
  origin: Origin;
  windowID: number;
}

const [windows, setWindows] = createStore<Win[]>([]);
const [focused, setFocused] = createSignal<FocusRef | null>(null);
const [desktop, setDesktop] = createSignal<DesktopMount | null>(null);

// isFocused reports whether a given (origin,windowID) is the focused one.
export function isFocused(ref: { origin: Origin; windowID: number }): boolean {
  const f = focused();
  return f != null && f.origin === ref.origin && f.windowID === ref.windowID;
}

// originForWindow resolves which router owns a windowID, for the
// window.wash WM intents that are still addressed by bare id (the session
// chrome's taskbar). Unambiguous while ids are unique in the store; the
// id-collision case across origins is handled when remote clients land
// (M1f) by addressing intents with their origin directly.
export function originForWindow(windowID: number): Origin {
  return windows.find((w) => w.windowID === windowID)?.origin ?? LOCAL_ORIGIN;
}

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
export function raiseLocal(origin: Origin, windowID: number): void {
  setWindows((w) => w.origin === origin && w.windowID === windowID, 'z', nextZ(windows));
  setFocused({ origin, windowID });
}

// moveLocal / resizeLocal write x/y/w/h to the store immediately so
// the FloatingWindow can clear its drag-or-resize override without
// snapping back to the pre-commit position while waiting for the
// router's session.patch. The router's patch is canonical and will
// overwrite these moments later — usually with the same values.
export function moveLocal(origin: Origin, windowID: number, x: number, y: number): void {
  setWindows((w) => w.origin === origin && w.windowID === windowID, { x, y });
}

export function resizeLocal(origin: Origin, windowID: number, w: number, h: number): void {
  setWindows((win) => win.origin === origin && win.windowID === windowID, { w, h });
}

export function mountDesktop(d: DesktopMount): void {
  setDesktop(d);
}

export function unmountDesktop(): void {
  setDesktop(null);
}

// fromSessionWindow projects the wire shape onto the local Win, stamping
// the origin of the router the snapshot/patch arrived from.
function fromSessionWindow(sw: SessionWindow, origin: Origin): Win {
  return {
    origin,
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
    chromeless: sw.chromeless,
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
  origin: Origin,
  sessionWins: SessionWindow[],
  waitForBundle: (instanceID: string) => Promise<void>,
): void {
  const keep = new Set<number>();
  for (const sw of sessionWins) {
    keep.add(sw.window_id);
  }
  // Drop windows of THIS origin that aren't in the new snapshot; other
  // origins' windows are untouched (a snapshot is authoritative only for
  // the router it came from). This is the load-bearing multi-router line:
  // without the origin guard, B's reconnect snapshot would wipe A's
  // windows.
  setWindows((prev) => prev.filter((w) => w.origin !== origin || keep.has(w.windowID)));

  for (const sw of sessionWins) {
    const w = fromSessionWindow(sw, origin);
    mountWhenReady(w, waitForBundle, 'snapshot');
  }
  // Reconcile focus to THIS origin's claim. A claim → focus it; no claim →
  // clear focus only if the currently-focused window belongs to this
  // origin (another origin's focus is left intact). See wm-focus.
  const claim = focusFromSnapshot(sessionWins);
  if (claim != null) {
    setFocused({ origin, windowID: claim });
  } else if (focused()?.origin === origin) {
    setFocused(null);
  }
}

// applySessionPatch applies a batch of mutations in order. Upserts
// wait for the bundle if the instance hasn't been declared yet.
export function applySessionPatch(
  origin: Origin,
  patches: SessionPatch[],
  waitForBundle: (instanceID: string) => Promise<void>,
): void {
  for (const p of patches) {
    if (p.op === 'window.upsert' && p.window) {
      const w = fromSessionWindow(p.window, origin);
      const existed = windows.find((x) => x.origin === origin && x.windowID === w.windowID) != null;
      if (existed) {
        upsertWindow(w);
      } else {
        // First sight of this window — mount when its element is ready
        // (immediately for built-ins, else after its bundle lands).
        mountWhenReady(w, waitForBundle, 'patch');
      }
      if (w.state === 'minimized' && isFocused(w)) {
        setFocused(null);
      } else if (p.window.focused) {
        setFocused({ origin, windowID: w.windowID });
      }
    } else if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      const id = p.window_id;
      // Crashed windows survive the router-side delete patch — they
      // become FE-only tombstones the user dismisses by clicking
      // close. The router has already torn down its side; we just
      // hold the geometry + crash info until the user is done.
      const w = windows.find((x) => x.origin === origin && x.windowID === id);
      if (w?.crashed) continue;
      setWindows((prev) => prev.filter((x) => !(x.origin === origin && x.windowID === id)));
      if (isFocused({ origin, windowID: id })) setFocused(null);
    }
  }
}

// markCrashed flips the tombstone bit on the matching window.
// Called from the shell's app.crashed handler in main.tsx. Idempotent.
// If the window isn't currently in the store (rare race: the BE
// crashed before its window-upsert reached the shell), this is a
// no-op — there's nothing to tombstone.
export function markCrashed(origin: Origin, instanceID: string, info: CrashInfo): void {
  const idx = windows.findIndex((w) => w.origin === origin && w.instanceID === instanceID);
  if (idx < 0) return;
  setWindows(idx, 'crashed', info);
}

// dismissCrashed removes a crash tombstone from the WM store. The
// close button on a crashed window calls this directly rather than
// sending window.close_clicked — the router-side state is already
// gone.
export function dismissCrashed(origin: Origin, windowID: number): void {
  setWindows((prev) => prev.filter((w) => !(w.origin === origin && w.windowID === windowID)));
  if (isFocused({ origin, windowID })) setFocused(null);
}

function upsertWindow(w: Win): void {
  const idx = windows.findIndex((x) => x.origin === w.origin && x.windowID === w.windowID);
  if (idx < 0) {
    setWindows((prev) => [...prev, w]);
  } else {
    setWindows(idx, w);
  }
}
