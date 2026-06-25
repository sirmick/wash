// createAppBus — the wash:msg / wash:state listener + send() plumbing that
// every windowed app otherwise hand-rolls (and must remember to tear down).
// Centralizing it removes ~19 copies of the addEventListener/onCleanup dance
// and the per-app `send` wrapper, and makes "forgot the onCleanup" impossible.
//
// Request/reply correlation is deliberately NOT here: apps that need it
// (fm, edit) layer @wash/fs-client's createBus on top of this send(). This
// stays the thin transport.

import { onCleanup } from 'solid-js';
import type { WashAppProps } from './define-app';

// AppBusMessage is the shape of a wash:msg detail — a tagged BE→FE message.
export interface AppBusMessage {
  kind: string;
  [k: string]: unknown;
}

// AppBusMsgBound is the minimal constraint for createAppBus's message type
// param: just a `kind` discriminant. A concrete discriminated union (whose
// members carry only their own fields, no index signature) satisfies this
// even though it isn't assignable to AppBusMessage's open index signature.
export type AppBusMsgBound = { kind: string };

// AppBusOptions is generic over the BE→FE message type so an app can pass
// its own discriminated union (e.g. `{ kind: 'snapshot'; … } | …`) and have
// each onMsg branch narrow on `kind`. Defaults to the open AppBusMessage for
// apps that haven't typed their wire shape.
export interface AppBusOptions<M extends AppBusMsgBound = AppBusMessage> {
  /** Invoked for every wash:msg CustomEvent (a BE→FE app message). */
  onMsg?: (m: M) => void;
  /** Invoked for every wash:state CustomEvent (persisted FE-state restore). */
  onState?: (state: unknown) => void;
  /**
   * Debounce window (ms) for saveState() coalescing. Bursts of view-state
   * changes (typing in a filter, dragging a splitter) collapse to one
   * router round-trip. Default 200. Set 0 to send on the next tick with
   * no coalescing.
   */
  saveDebounceMs?: number;
}

export interface AppBus {
  /** Fire-and-forget send to this app instance's BE half. */
  send: (msg: unknown) => void;
  /**
   * Persist this app's opaque view-state blob router-side (debounced).
   * The router stores it per instance; the shell redelivers the latest
   * blob as the wash:state restore event (opts.onState) on every
   * (re)mount, so reconnect and new streams reconstitute the window from
   * it. Pair with sdk.HandlePersist on the BE. Last value wins within the
   * debounce window — build a fresh snapshot at each call site.
   */
  saveState: (state: unknown) => void;
  /** Flush any pending debounced saveState immediately (e.g. before a known teardown). */
  flushState: () => void;
}

// createAppBus binds send() to this instance and registers the requested
// listeners with automatic onCleanup. Call it once during component setup
// (the top of the App body) so onCleanup attaches to the component owner.
export function createAppBus<M extends AppBusMsgBound = AppBusMessage>(
  props: Pick<WashAppProps, 'instance' | 'host'>,
  opts: AppBusOptions<M> = {},
): AppBus {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  if (opts.onMsg) {
    const onMsg = opts.onMsg;
    const handler = (ev: Event) => onMsg((ev as CustomEvent).detail as M);
    props.host.addEventListener('wash:msg', handler);
    onCleanup(() => props.host.removeEventListener('wash:msg', handler));
  }
  if (opts.onState) {
    const onState = opts.onState;
    const handler = (ev: Event) => onState((ev as CustomEvent).detail);
    props.host.addEventListener('wash:state', handler);
    onCleanup(() => props.host.removeEventListener('wash:state', handler));
  }

  // Debounced view-state persistence. We hold the latest blob (a sentinel
  // distinguishes "nothing pending" from a legitimately null/undefined
  // blob) and emit the canonical { kind: 'save_state', state } envelope
  // that sdk.HandlePersist consumes on the BE.
  const debounceMs = opts.saveDebounceMs ?? 200;
  const NOTHING = Symbol('no-pending-state');
  let pending: unknown = NOTHING;
  let saveTimer: ReturnType<typeof setTimeout> | null = null;
  const flushState = () => {
    if (saveTimer != null) {
      clearTimeout(saveTimer);
      saveTimer = null;
    }
    if (pending !== NOTHING) {
      const state = pending;
      pending = NOTHING;
      send({ kind: 'save_state', state });
    }
  };
  const saveState = (state: unknown) => {
    pending = state;
    if (saveTimer != null) clearTimeout(saveTimer);
    saveTimer = setTimeout(flushState, debounceMs);
  };
  // Don't leave an unsent blob behind on unmount — a remount restores from
  // whatever the router last received, so flush the tail.
  onCleanup(flushState);

  return { send, saveState, flushState };
}
