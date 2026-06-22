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

export interface AppBusOptions {
  /** Invoked for every wash:msg CustomEvent (a BE→FE app message). */
  onMsg?: (m: AppBusMessage) => void;
  /** Invoked for every wash:state CustomEvent (persisted FE-state restore). */
  onState?: (state: unknown) => void;
}

export interface AppBus {
  /** Fire-and-forget send to this app instance's BE half. */
  send: (msg: unknown) => void;
}

// createAppBus binds send() to this instance and registers the requested
// listeners with automatic onCleanup. Call it once during component setup
// (the top of the App body) so onCleanup attaches to the component owner.
export function createAppBus(
  props: Pick<WashAppProps, 'instance' | 'host'>,
  opts: AppBusOptions = {},
): AppBus {
  const send = (msg: unknown) => window.wash.sendAppMsg(props.instance, msg);

  if (opts.onMsg) {
    const onMsg = opts.onMsg;
    const handler = (ev: Event) => onMsg((ev as CustomEvent).detail as AppBusMessage);
    props.host.addEventListener('wash:msg', handler);
    onCleanup(() => props.host.removeEventListener('wash:msg', handler));
  }
  if (opts.onState) {
    const onState = opts.onState;
    const handler = (ev: Event) => onState((ev as CustomEvent).detail);
    props.host.addEventListener('wash:state', handler);
    onCleanup(() => props.host.removeEventListener('wash:state', handler));
  }

  return { send };
}
