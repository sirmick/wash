// Shell-host API exposed to mounted app elements as `window.wash`.
// Apps inside custom elements use this to listen to chrome state
// (catalog, open windows) and to request actions (spawn via app_msg,
// focus, close).

import { type Origin, parseInstanceId, compoundInstanceId, compoundChannelId } from './clients';

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

// PanelDesc is one app-supplied settings panel from the catalog's
// `panels` list (wire.ShellPanelDesc, passed through verbatim so field
// names match the wire JSON). The settings app renders a section per
// descriptor and calls window.wash.loadSettingsPanel(app_id) to define
// + mount the element on demand.
export interface PanelDesc {
  app_id: string;
  section: string;
  element: string;
  icon?: string;
  order?: number;
}

export interface WindowInfo {
  /** Origin (router) the window belongs to; LOCAL for the shell's own. */
  origin: Origin;
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

// rawSubscribers maps an ORIGIN-SCOPED channel key → callback for
// incoming raw bytes. Keyed by compoundChannelId(origin, channel) so a
// remote host's channel 5 never collides with a local app's channel 5
// (docs/REMOTE.md §4 — per-origin app wiring). Elements register via
// window.wash.openRawChannelFor; the per-client WS dispatch routes
// matching frames through with that client's origin.
const rawSubscribers = new Map<string, (bytes: Uint8Array) => void>();
// pendingRaw queues bytes that arrive on a channel before any
// subscriber registers (the BE typically writes its first byte the
// moment the router binds the channel, ahead of the BE → FE
// app_msg that tells the FE the channel id). Same origin-scoped key.
const pendingRaw = new Map<string, Uint8Array[]>();

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
export function replaceSavedStates(origin: Origin, states: Record<string, unknown> | undefined): void {
  // app_state from a session.snapshot is keyed by BARE instance id and is
  // authoritative only for the router it came from. Drop this origin's
  // existing (compound-keyed) entries, then set the new ones compound —
  // other origins' saved states are untouched.
  for (const k of [...savedStates.keys()]) {
    if (parseInstanceId(k).origin === origin) savedStates.delete(k);
  }
  if (states) {
    for (const [bare, v] of Object.entries(states)) {
      if (v != null) savedStates.set(compoundInstanceId(origin, bare), v);
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

export function deliverRaw(origin: Origin, channelID: number, bytes: Uint8Array): void {
  const key = compoundChannelId(origin, channelID);
  const cb = rawSubscribers.get(key);
  if (cb) {
    cb(bytes);
    return;
  }
  let q = pendingRaw.get(key);
  if (!q) {
    q = [];
    pendingRaw.set(key, q);
  }
  q.push(bytes);
}

export function subscribeRaw(origin: Origin, channelID: number, cb: (bytes: Uint8Array) => void): () => void {
  const key = compoundChannelId(origin, channelID);
  rawSubscribers.set(key, cb);
  const q = pendingRaw.get(key);
  if (q) {
    pendingRaw.delete(key);
    for (const b of q) cb(b);
  }
  return () => {
    if (rawSubscribers.get(key) === cb) {
      rawSubscribers.delete(key);
    }
  };
}

export function closeRawSubscriber(origin: Origin, channelID: number): void {
  const key = compoundChannelId(origin, channelID);
  rawSubscribers.delete(key);
  pendingRaw.delete(key);
}

// resyncSubscribers maps an origin-scoped channel key → a callback that
// resets the channel's terminal before a replay. The router sends a
// channel.resync (docs/PTY_ROBUST.md, Fix B) when a terminal that went
// "behind" can deliver again: live output was suppressed to avoid a torn
// stream, and a clean reset + realigned snapshot now restores it. The
// callback runs synchronously, BEFORE the snapshot bytes that follow on
// the raw channel (same WS message order), so the snapshot renders into a
// reset terminal. Unlike rawSubscribers there is no pending queue — a
// resync for an unsubscribed channel is a harmless no-op.
const resyncSubscribers = new Map<string, () => void>();

export function subscribeResync(origin: Origin, channelID: number, cb: () => void): () => void {
  const key = compoundChannelId(origin, channelID);
  resyncSubscribers.set(key, cb);
  return () => {
    if (resyncSubscribers.get(key) === cb) {
      resyncSubscribers.delete(key);
    }
  };
}

export function deliverResync(origin: Origin, channelID: number): void {
  resyncSubscribers.get(compoundChannelId(origin, channelID))?.();
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
  // Optional: a child-surface (menu/dropdown) overlay channel for this
  // window. Implemented by <wash-app-display>; see DISPLAY.md §12 (M3).
  attachPopupChannel?(channelID: number): void;
}

// Window ids are per-router, so a remote display window can share an id with
// a local one. Key the registries by (origin, windowID) — same reason raw
// channels use compoundChannelId — so a remote wash-display window never
// clobbers a local one's element/channel (docs/REMOTE.md R2). Reuses the
// channel codec (origin + int → unique string); parseInstanceId reverses it.
function winKey(origin: Origin, windowID: number): string {
  return compoundChannelId(origin, windowID);
}

const displayWindows = new Map<string, DisplayElement>();
const videoChannelForWindow = new Map<string, number>();
// Popup channels that arrived before the parent element registered, per
// (origin, window).
const pendingPopupChannels = new Map<string, number[]>();

export function registerDisplayWindow(origin: Origin, windowID: number, el: DisplayElement): void {
  const key = winKey(origin, windowID);
  displayWindows.set(key, el);
  const ch = videoChannelForWindow.get(key);
  if (ch !== undefined) {
    try {
      el.attachVideoChannel(ch);
    } catch (e) {
      console.error('wash: attachVideoChannel (register):', e);
    }
  }
  const pops = pendingPopupChannels.get(key);
  if (pops && el.attachPopupChannel) {
    pendingPopupChannels.delete(key);
    for (const c of pops) {
      try {
        el.attachPopupChannel(c);
      } catch (e) {
        console.error('wash: attachPopupChannel (register):', e);
      }
    }
  }
}

export function unregisterDisplayWindow(origin: Origin, windowID: number, el: DisplayElement): void {
  // Only drop if the registered element is still us — guards against a
  // remount that already re-registered before our disconnect ran.
  const key = winKey(origin, windowID);
  if (displayWindows.get(key) === el) {
    displayWindows.delete(key);
  }
}

// bindVideoChannel records (and, if the element is mounted, attaches)
// the video channel for a window. Called from main.tsx's channel.bind
// handler when kind === wire.ChannelKindVideo ("video"), scoped to the
// origin of the router that bound it.
export function bindVideoChannel(origin: Origin, windowID: number, channelID: number): void {
  const key = winKey(origin, windowID);
  videoChannelForWindow.set(key, channelID);
  const el = displayWindows.get(key);
  if (el) {
    try {
      el.attachVideoChannel(channelID);
    } catch (e) {
      console.error('wash: attachVideoChannel (bind):', e);
    }
  }
}

// bindPopupChannel routes a child-surface (menu/dropdown) overlay channel
// to the PARENT window's display element. Called from main.tsx's
// channel.bind handler when kind === "video-popup". Origin-scoped like the
// video registry (window ids are per-router). If the element isn't mounted
// yet, the channel is stashed and replayed on register.
export function bindPopupChannel(origin: Origin, parentWindowID: number, channelID: number): void {
  const key = winKey(origin, parentWindowID);
  const el = displayWindows.get(key);
  if (el && el.attachPopupChannel) {
    try {
      el.attachPopupChannel(channelID);
    } catch (e) {
      console.error('wash: attachPopupChannel (bind):', e);
    }
    return;
  }
  const list = pendingPopupChannels.get(key) ?? [];
  list.push(channelID);
  pendingPopupChannels.set(key, list);
}

// forgetVideoChannel clears the stashed video channel for a window of this
// origin. Called on channel.unbind so a later rebind on the same window id
// doesn't replay a dead channel to a freshly-mounted element. Scoped to the
// origin so an unbind on B can't drop a local window's identical channel id.
export function forgetVideoChannel(origin: Origin, channelID: number): void {
  for (const [key, chID] of videoChannelForWindow) {
    if (chID === channelID && parseInstanceId(key).origin === origin) {
      videoChannelForWindow.delete(key);
      break;
    }
  }
}
