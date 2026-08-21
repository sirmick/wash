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
import { clampViewport, viewportForRect } from './viewport-math';
import { focusFromSnapshot } from './wm-focus';
import { beginWindowLoad, endWindowLoad } from './boot';

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
  // z is the router's per-router stacking value (authoritative WITHIN an
  // origin). It is NOT comparable across origins — each router counts from
  // its own zero — so it can't drive cross-desktop stacking on its own.
  z: number;
  // gz is the FE-arbitrated GLOBAL stacking order: a single monotonic
  // counter bumped whenever any window (any origin) is raised/focused or
  // first appears. window.tsx renders z-index from gz, so "bring to front"
  // works across origins where the colliding per-router z cannot (a remote
  // window's focus would otherwise be overwritten by B's small B-local z and
  // sink behind local windows). Below the chrome z-band by construction.
  gz: number;
  state: WinState;
  // Pre-min/max geometry — kept around so a future "restore" call can
  // return the window to its user-set frame even after a chain of
  // min → max → restore. Tracked router-side; shells just mirror.
  restoreX?: number;
  restoreY?: number;
  restoreW?: number;
  restoreH?: number;
  // Client size hints (0/absent = unset); the interactive resize clamps to
  // them. Mirror of SessionWindow.min_w/… (router-owned).
  minW?: number;
  minH?: number;
  maxW?: number;
  maxH?: number;
  // isRoot is router-attested; renders the red ROOT stripe in the
  // titlebar. See SessionWindow.is_root in main.tsx.
  isRoot?: boolean;
  // chromeless drops the wash titlebar + border so the guest surface
  // (e.g. Webamp) fills the frame and draws its own chrome. Mirrors
  // the app manifest's WindowHints.Chromeless.
  chromeless?: boolean;
  // attention is router-owned: the owning app said this window needs the
  // human, and the router clears it the moment the window takes focus.
  // The taskbar pill pulses for it (docs/AGENT_UX.md N6).
  attention?: boolean;
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
// focused is a (origin,windowID) ref, so it must compare by VALUE — without
// this equals, re-focusing the already-focused window sets a fresh object each
// time, firing the signal and re-rendering every focus-dependent view (e.g.
// the taskbar pills). That recreated a pill's DOM between the two clicks of a
// dblclick, so onDblClick never fired (taskbar pill → snap-to-viewport broke).
const [focused, setFocused] = createSignal<FocusRef | null>(null, {
  equals: (a, b) =>
    a === b || (a != null && b != null && a.origin === b.origin && a.windowID === b.windowID),
});
const [desktop, setDesktop] = createSignal<DesktopMount | null>(null);

// Global stacking counter — the FE's cross-origin z arbiter. Bumped on every
// raise/focus/first-appearance so the most-recently-raised window has the
// highest gz regardless of which router owns it. window.tsx renders z-index
// from gz; see Win.gz.
let gzCounter = 0;
function bumpGz(): number {
  return ++gzCounter;
}

// raiseGz lifts a window to the top of the global stack without touching
// focus (focus is set separately by the caller). No-op if the window isn't
// in the store yet (e.g. a snapshot focus claim for a window still waiting on
// its bundle — it lands on top anyway via its first-insert gz).
function raiseGz(origin: Origin, windowID: number): void {
  setWindows((w) => w.origin === origin && w.windowID === windowID, 'gz', bumpGz());
}

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
  raiseGz(origin, windowID);
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

// windowById returns the live window record for (origin, windowID), or
// undefined. Used by the display element's move bridge to read a window's
// current x/y when a CSD guest requests an interactive move (M8). Origin-
// scoped because window ids are per-router (docs/REMOTE.md R2).
export function windowById(origin: Origin, windowID: number): Win | undefined {
  return windows.find((w) => w.origin === origin && w.windowID === windowID);
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
    // gz is FE-owned; a real value is stamped at insert time (upsertWindow).
    gz: 0,
    state: sw.state,
    restoreX: sw.restore_x,
    restoreY: sw.restore_y,
    restoreW: sw.restore_w,
    restoreH: sw.restore_h,
    minW: sw.min_w,
    minH: sw.min_h,
    maxW: sw.max_w,
    maxH: sw.max_h,
    isRoot: sw.is_root,
    chromeless: sw.chromeless,
    attention: sw.attention,
  };
}

// A bundle-backed window's mount is deferred until its bundle arrives (up to
// ~10s). Meanwhile a reconnect snapshot or a delete can supersede it — but
// the deferred window isn't in the store yet, so applySessionSnapshot's
// synchronous filter and window.delete's filter both miss it, and the late
// upsert lands an unclosable ghost the router no longer knows about
// (REVIEW-RECONNECT M5). We guard the deferred upsert two ways:
//
//   - snapshotEpoch: a per-origin counter bumped on every snapshot (an
//     authoritative reset). A deferred mount captures the epoch it was
//     scheduled under; if the live epoch has since advanced, a newer snapshot
//     superseded it — and that snapshot re-scheduled the window if it still
//     wanted it — so the stale mount is dropped.
//   - pendingMounts + a per-record cancelled flag: a window.delete for a
//     still-pending window flips its record's cancelled bit so the resolve
//     drops the upsert. Keyed by (origin,windowID); the closure holds its own
//     record so a re-schedule for the same key can't cross wires.
const snapshotEpoch = new Map<Origin, number>();
interface PendingMount {
  epoch: number;
  cancelled: boolean;
}
const pendingMounts = new Map<string, PendingMount>();

function winKey(origin: Origin, windowID: number): string {
  return `${origin}:${windowID}`;
}

// cancelPendingMount marks a still-deferred mount for (origin,windowID) so its
// resolve won't land it. No-op if nothing is pending for that key.
function cancelPendingMount(origin: Origin, windowID: number): void {
  const rec = pendingMounts.get(winKey(origin, windowID));
  if (rec) rec.cancelled = true;
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
  // Element isn't defined yet — its bundle is in flight. Show the busy
  // cursor (index.html `.wash-launching`) until the window mounts, so a
  // freshly-launched app gives immediate "starting…" feedback.
  const key = winKey(w.origin, w.windowID);
  const rec: PendingMount = { epoch: snapshotEpoch.get(w.origin) ?? 0, cancelled: false };
  pendingMounts.set(key, rec); // overwrites any prior pending mount for this key
  beginWindowLoad();
  waitForBundle(w.instanceID)
    .then(() => {
      // Only mount if THIS scheduling is still the current one for the key,
      // wasn't cancelled by a delete, and its snapshot epoch is still live —
      // otherwise it's a superseded ghost (REVIEW-RECONNECT M5).
      if (pendingMounts.get(key) !== rec) return; // a newer schedule replaced us
      if (rec.cancelled) return; // deleted while the bundle was in flight
      if ((snapshotEpoch.get(w.origin) ?? 0) !== rec.epoch) return; // newer snapshot
      upsertWindow(w);
    })
    .catch((err) => console.error(`wash: ${where} window mount:`, err))
    .finally(() => {
      if (pendingMounts.get(key) === rec) pendingMounts.delete(key);
      endWindowLoad();
    });
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
  // A snapshot is an authoritative reset for this origin: bump its epoch so
  // any mount still deferred under the old epoch is treated as superseded
  // when it resolves (REVIEW-RECONNECT M5). Windows the snapshot still wants
  // are re-scheduled below under the new epoch.
  snapshotEpoch.set(origin, (snapshotEpoch.get(origin) ?? 0) + 1);

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
  //
  // But a snapshot arrives on every (re)connect, and a router keeps
  // attesting its own focused window from its local point of view. If we
  // honoured that claim unconditionally, a remote host dropping and
  // reconnecting would re-raise its window to the top of the global stack
  // and steal focus from whatever the user is actively working in on
  // another origin — the window "flashes to the foreground" on every blip.
  // So only adopt the claim when it wouldn't yank focus away from a
  // different origin; existing remote windows keep their gz (upsertWindow
  // preserves it) and stay put, while genuinely new windows still land on
  // top via their first-insert gz.
  const claim = focusFromSnapshot(sessionWins);
  const cur = focused();
  const otherOriginFocused = cur != null && cur.origin !== origin;
  if (claim != null && !otherOriginFocused) {
    setFocused({ origin, windowID: claim });
    raiseGz(origin, claim);
  } else if (claim == null && cur?.origin === origin) {
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
        // The router raised this window (this or another client focused it) —
        // lift it to the top of the global stack so cross-origin focus shows.
        raiseGz(origin, w.windowID);
      }
    } else if (p.op === 'window.delete' && typeof p.window_id === 'number') {
      const id = p.window_id;
      // Crashed windows survive the router-side delete patch — they
      // become FE-only tombstones the user dismisses by clicking
      // close. The router has already torn down its side; we just
      // hold the geometry + crash info until the user is done.
      const w = windows.find((x) => x.origin === origin && x.windowID === id);
      if (w?.crashed) continue;
      // A window whose bundle is still in flight isn't in the store yet, so
      // the filter below misses it — cancel its pending mount so the late
      // upsert can't resurrect it as a ghost (REVIEW-RECONNECT M5).
      cancelPendingMount(origin, id);
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

// dropOrigin removes every window belonging to a remote origin — called
// when a host disconnects (docs/REMOTE.md §6.1/§9): the supervisor reports
// the host down and the shell detaches its RouterClient, so its frozen
// windows must leave the desktop. Clears focus if it pointed at this host.
// LOCAL is never dropped (the seat's own desktop).
export function dropOrigin(origin: Origin): void {
  if (origin === LOCAL_ORIGIN) return;
  setWindows((prev) => prev.filter((w) => w.origin !== origin));
  if (focused()?.origin === origin) setFocused(null);
}

function upsertWindow(w: Win): void {
  const idx = windows.findIndex((x) => x.origin === w.origin && x.windowID === w.windowID);
  if (idx < 0) {
    // A new window starts on top of the global stack.
    setWindows((prev) => [...prev, { ...w, gz: bumpGz() }]);
  } else {
    // Router-driven update (geometry/title/state). Preserve the FE's global
    // stacking value — only an explicit raise (raiseLocal / a focused patch /
    // a snapshot focus claim) changes gz, never a plain geometry patch.
    setWindows(idx, { ...w, gz: windows[idx].gz });
  }
}
