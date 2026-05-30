// Shell-host API exposed to mounted app elements as `window.wash`.
// Apps inside custom elements use this to listen to chrome state
// (catalog, open windows) and to request actions (spawn via app_msg,
// focus, close).

export interface CatalogApp {
  id: string;
  name: string;
  icon?: string;
  /** Brand color (CSS color) for the launcher icon. Falls back to a
   * deterministic hash of `id` when unset, so no row stays mono. */
  accent?: string;
  surface: string;
  instancing: string;
  disabled?: boolean;
  reason?: string;
}

export interface WindowInfo {
  windowID: number;
  instanceID: string;
  element: string;
  icon?: string;
  title: string;
  focused: boolean;
  state: 'normal' | 'minimized' | 'maximized';
  // Global (3W×3H plane) coords and dimensions. The pager renders
  // window outlines from these; the rest of the chrome ignores them.
  x: number;
  y: number;
  w: number;
  h: number;
  // viewport cell that owns this window (derived from center). The
  // taskbar pill uses this for dblclick snap-to-viewport.
  viewport: { vx: number; vy: number };
}

export type Listener<T> = (v: T) => void;

// Sub is a tiny pub/sub holding the current value plus a set of
// listeners. `on` fires the listener immediately with the current
// value so subscribers don't need a separate "get".
export class Sub<T> {
  private current: T;
  private listeners = new Set<Listener<T>>();

  constructor(initial: T) {
    this.current = initial;
  }

  get value(): T {
    return this.current;
  }

  set(v: T): void {
    this.current = v;
    for (const l of this.listeners) l(v);
  }

  on(cb: Listener<T>): () => void {
    this.listeners.add(cb);
    cb(this.current);
    return () => {
      this.listeners.delete(cb);
    };
  }
}

// mountedElements tracks the live custom element for each declared
// instance id. registerMountedElement is called from Desktop /
// FloatingWindow once an element is in the DOM; deliverToInstance
// queues messages that arrive before the element mounts so nothing is
// dropped during the race between addWindow and Solid's onMount.
const mountedElements = new Map<string, HTMLElement>();
const pendingMessages = new Map<string, unknown[]>();

// Latest saved FE-state blob per instance. Delivered to the element
// as a `wash:state` CustomEvent on (re)mount. Updated by router's
// session.snapshot / session.patch deliveries.
const savedStates = new Map<string, unknown>();

// rawSubscribers maps channel id → callback for incoming raw bytes.
// Elements register via window.wash.openRawChannel; the shell's WS
// handler dispatches matching frames through.
const rawSubscribers = new Map<number, (bytes: Uint8Array) => void>();
// pendingRaw queues bytes that arrive on a channel before any
// subscriber registers (the BE typically writes its first byte the
// moment the router binds the channel, ahead of the BE → FE
// app_msg that tells the FE the channel id).
const pendingRaw = new Map<number, Uint8Array[]>();

export function registerMountedElement(instanceID: string, el: HTMLElement): void {
  mountedElements.set(instanceID, el);
  // Deliver the latest saved state first — apps initialize by listening
  // for wash:state, so it must arrive before any subsequent wash:msg.
  // null means "no prior state": apps should treat it as first launch.
  const state = savedStates.has(instanceID) ? savedStates.get(instanceID) : null;
  el.dispatchEvent(new CustomEvent('wash:state', { detail: state, bubbles: false }));

  const q = pendingMessages.get(instanceID);
  if (q) {
    pendingMessages.delete(instanceID);
    for (const data of q) {
      el.dispatchEvent(new CustomEvent('wash:msg', { detail: data, bubbles: false }));
    }
  }
}

// setSavedState updates the cached blob for an instance. We do NOT
// dispatch wash:state to a live element — that would echo the app's
// own writes back to itself (the BE save → router patch → setSavedState
// chain runs on every persist) and trigger a feedback loop. wash:state
// fires only on (re)mount via registerMountedElement.
//
// Tradeoff: a second browser tab viewing the same session sees
// app-state changes only on its next refresh, not in realtime. Worth
// it for the much simpler single-writer story.
export function setSavedState(instanceID: string, state: unknown): void {
  if (state == null) {
    savedStates.delete(instanceID);
  } else {
    savedStates.set(instanceID, state);
  }
}

// clearSavedState drops the cached blob (instance destroyed).
export function clearSavedState(instanceID: string): void {
  savedStates.delete(instanceID);
}

// replaceSavedStates replaces the entire cache. Used on snapshot
// processing to drop any stale entries from instances the router no
// longer knows about.
export function replaceSavedStates(states: Record<string, unknown> | undefined): void {
  savedStates.clear();
  if (states) {
    for (const [k, v] of Object.entries(states)) {
      if (v != null) savedStates.set(k, v);
    }
  }
}

export function unregisterMountedElement(instanceID: string): void {
  mountedElements.delete(instanceID);
  pendingMessages.delete(instanceID);
}

export function deliverToInstance(instanceID: string, data: unknown): void {
  const el = mountedElements.get(instanceID);
  if (el) {
    el.dispatchEvent(new CustomEvent('wash:msg', { detail: data, bubbles: false }));
    return;
  }
  let q = pendingMessages.get(instanceID);
  if (!q) {
    q = [];
    pendingMessages.set(instanceID, q);
  }
  q.push(data);
}

// Raw channel API exposed via window.wash.openRawChannel /
// writeRaw. v0.1 uses one-callback-per-channel; if a future need
// arises we can move to a small EventTarget per channel.

export function deliverRaw(channelID: number, bytes: Uint8Array): void {
  const cb = rawSubscribers.get(channelID);
  if (cb) {
    cb(bytes);
    return;
  }
  let q = pendingRaw.get(channelID);
  if (!q) {
    q = [];
    pendingRaw.set(channelID, q);
  }
  q.push(bytes);
}

export function subscribeRaw(channelID: number, cb: (bytes: Uint8Array) => void): () => void {
  rawSubscribers.set(channelID, cb);
  const q = pendingRaw.get(channelID);
  if (q) {
    pendingRaw.delete(channelID);
    for (const b of q) cb(b);
  }
  return () => {
    if (rawSubscribers.get(channelID) === cb) {
      rawSubscribers.delete(channelID);
    }
  };
}

export function closeRawSubscriber(channelID: number): void {
  rawSubscribers.delete(channelID);
  pendingRaw.delete(channelID);
}

// ---- Display window ↔ video channel registry ----
//
// The built-in <wash-app-display> element (wash-app-display.ts) renders
// one window's video stream. wash-display windows carry element
// "wash-app-display" with an empty app bundle, so the element is a shell
// built-in keyed by WINDOW id (not instance id — a single background
// wash-display instance owns many display windows).
//
// The router announces a window's video channel via channel.bind with
// kind="video" {channel_id, window_id}. main.tsx forwards that here as
// bindVideoChannel(window_id, channel_id). The element registers itself
// on mount via registerDisplayWindow(window_id, el). Either order works:
//   - bind before mount → channel id is stashed; replayed on register.
//   - mount before bind → element waits; attached when the bind lands.
// This mirrors the pendingMessages / pendingRaw pattern above.
//
// Actual frame bytes still flow through deliverRaw(channel_id, …); the
// element subscribes via subscribeRaw(channel_id) once attached.

interface DisplayElement extends HTMLElement {
  attachVideoChannel(channelID: number): void;
}

const displayWindows = new Map<number, DisplayElement>();
const videoChannelForWindow = new Map<number, number>();

export function registerDisplayWindow(windowID: number, el: DisplayElement): void {
  displayWindows.set(windowID, el);
  const ch = videoChannelForWindow.get(windowID);
  if (ch !== undefined) {
    try {
      el.attachVideoChannel(ch);
    } catch (e) {
      console.error('wash: attachVideoChannel (register):', e);
    }
  }
}

export function unregisterDisplayWindow(windowID: number, el: DisplayElement): void {
  // Only drop if the registered element is still us — guards against a
  // remount that already re-registered before our disconnect ran.
  if (displayWindows.get(windowID) === el) {
    displayWindows.delete(windowID);
  }
}

// bindVideoChannel records (and, if the element is mounted, attaches)
// the video channel for a window. Called from main.tsx's channel.bind
// handler when kind === wire.ChannelKindVideo ("video").
export function bindVideoChannel(windowID: number, channelID: number): void {
  videoChannelForWindow.set(windowID, channelID);
  const el = displayWindows.get(windowID);
  if (el) {
    try {
      el.attachVideoChannel(channelID);
    } catch (e) {
      console.error('wash: attachVideoChannel (bind):', e);
    }
  }
}

// forgetVideoChannel clears the stashed video channel for a window.
// Called on channel.unbind so a later rebind on the same window id
// doesn't replay a dead channel to a freshly-mounted element.
export function forgetVideoChannel(channelID: number): void {
  for (const [winID, chID] of videoChannelForWindow) {
    if (chID === channelID) {
      videoChannelForWindow.delete(winID);
      break;
    }
  }
}
