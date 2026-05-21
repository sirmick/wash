// Browser-side WebSocket transport: one wash frame per binary
// message (WIRE.md §1). Reconnects on close with backoff so dev-
// reload's `syscall.Exec` self-restart shows up as a brief
// "reconnecting…" banner instead of a permanently dead tab.

import { decodeFrame, encodeCtrl, encodeFrame, FLAG_END, decodeCtrl } from './wire';

export type CtrlHandler = (msg: any) => void;
export type RawHandler = (channelID: number, bytes: Uint8Array) => void;
export type StateHandler = (state: ConnState) => void;
export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed';

export class Conn {
  private ws!: WebSocket;
  private handler: CtrlHandler;
  private rawHandler: RawHandler;
  private stateHandlers = new Set<StateHandler>();
  private url: string;
  private opening: Promise<void>;
  private state: ConnState = 'connecting';
  private reconnectAttempts = 0;
  private closedByUser = false;

  constructor(url: string, handler: CtrlHandler, rawHandler: RawHandler) {
    this.url = url;
    this.handler = handler;
    this.rawHandler = rawHandler;
    this.opening = new Promise((resolve, reject) => {
      this.connect(resolve, reject);
    });
  }

  ready(): Promise<void> { return this.opening; }

  onState(fn: StateHandler): () => void {
    this.stateHandlers.add(fn);
    fn(this.state);
    return () => { this.stateHandlers.delete(fn); };
  }

  private setState(s: ConnState) {
    if (this.state === s) return;
    this.state = s;
    for (const fn of this.stateHandlers) fn(s);
  }

  private connect(resolveReady?: () => void, rejectReady?: (e: Error) => void) {
    this.setState(this.reconnectAttempts === 0 ? 'connecting' : 'reconnecting');
    this.ws = new WebSocket(this.url);
    this.ws.binaryType = 'arraybuffer';
    this.ws.onopen = () => {
      this.reconnectAttempts = 0;
      this.setState('open');
      resolveReady?.();
    };
    this.ws.onerror = () => {
      if (this.reconnectAttempts === 0) {
        rejectReady?.(new Error('ws error'));
      }
    };
    this.ws.onmessage = (ev) => this.onMessage(ev);
    this.ws.onclose = () => {
      if (this.closedByUser) {
        this.setState('closed');
        return;
      }
      // Backoff: 250ms, 500ms, 1s, 2s, then cap at 4s.
      const delay = Math.min(250 * 2 ** this.reconnectAttempts, 4000);
      this.reconnectAttempts += 1;
      this.setState('reconnecting');
      setTimeout(() => {
        if (this.closedByUser) return;
        this.connect();
      }, delay);
    };
  }

  private onMessage(ev: MessageEvent) {
    const f = decodeFrame(new Uint8Array(ev.data));
    if (f.channel !== 0) {
      this.rawHandler(f.channel, f.payload);
      return;
    }
    let msg: any;
    try {
      msg = decodeCtrl(f.payload);
    } catch (e) {
      console.error('wash: decode ctrl:', e);
      return;
    }
    this.handler(msg);
  }

  sendCtrl(msg: unknown): void {
    const payload = encodeCtrl(msg);
    this.ws.send(encodeFrame({ flags: FLAG_END, channel: 0, payload }));
  }

  sendRaw(channelID: number, payload: Uint8Array): void {
    this.ws.send(encodeFrame({ flags: FLAG_END, channel: channelID, payload }));
  }
}
