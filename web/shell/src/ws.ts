// Browser-side wash transport: one wash frame per binary message
// (WIRE.md §1). Reconnects on close with backoff so dev-reload's
// `syscall.Exec` self-restart shows up as a brief "reconnecting…"
// banner instead of a permanently dead tab.
//
// The default transport is a real WebSocket to the router's HTTP
// listener. With ?transport=virtio-console (v86 online demo) main.tsx
// hands Conn a factory that produces VirtioConsoleSocket instances
// instead — same SocketLike contract, different underlying byte
// stream.

import {
  CLASS_BULK,
  CLASS_INTERACTIVE,
  Class,
  decodeFrame,
  encodeCtrl,
  encodeFrame,
  flagsWithClass,
  FLAG_END,
  decodeCtrl,
} from './wire';
import type { SocketLike } from './virtio';

export type CtrlHandler = (msg: any) => void;
export type RawHandler = (channelID: number, bytes: Uint8Array) => void;
export type StateHandler = (state: ConnState) => void;
export type ConnState = 'connecting' | 'open' | 'reconnecting' | 'closed';

/** Factory returning a fresh SocketLike each call. Reconnect calls it. */
export type SocketFactory = () => SocketLike;

export class Conn {
  private ws!: SocketLike;
  private handler: CtrlHandler;
  private rawHandler: RawHandler;
  private stateHandlers = new Set<StateHandler>();
  private factory: SocketFactory;
  private opening: Promise<void>;
  private state: ConnState = 'connecting';
  private reconnectAttempts = 0;
  private closedByUser = false;

  /**
   * Construct from a URL (default: real WebSocket) or a SocketFactory
   * (e.g. VirtioConsoleSocket factory from `virtio.ts`).
   */
  constructor(urlOrFactory: string | SocketFactory, handler: CtrlHandler, rawHandler: RawHandler) {
    this.handler = handler;
    this.rawHandler = rawHandler;
    this.factory =
      typeof urlOrFactory === 'string'
        ? () => new WebSocket(urlOrFactory) as unknown as SocketLike
        : urlOrFactory;
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
    this.ws = this.factory();
    this.ws.binaryType = 'arraybuffer';
    this.ws.onopen = () => {
      this.reconnectAttempts = 0;
      this.setState('open');
      resolveReady?.();
    };
    this.ws.onerror = () => {
      if (this.reconnectAttempts === 0) {
        rejectReady?.(new Error('socket error'));
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
    const f = decodeFrame(new Uint8Array(ev.data as ArrayBuffer));
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

  /**
   * Send a JSON control message on channel 0. Class defaults to
   * Interactive — control frames are user-action / lifecycle by
   * nature. Callers in unusual cases (telemetry batches?) can pass
   * an explicit class.
   */
  sendCtrl(msg: unknown, cls: Class = CLASS_INTERACTIVE): void {
    const payload = encodeCtrl(msg);
    this.ws.send(encodeFrame({ flags: flagsWithClass(FLAG_END, cls), channel: 0, payload }));
  }

  /**
   * Send raw bytes on a dynamic channel. Class defaults to Bulk
   * because most raw streams from the FE side are large/streaming
   * (input pasting, file uploads); keystroke-style interactive
   * raw flows (e.g. wash-term's one-byte keystroke writes) should
   * pass CLASS_INTERACTIVE explicitly so they don't queue behind
   * other apps' bulk traffic on the way to the router.
   */
  sendRaw(channelID: number, payload: Uint8Array, cls: Class = CLASS_BULK): void {
    this.ws.send(encodeFrame({ flags: flagsWithClass(FLAG_END, cls), channel: channelID, payload }));
  }

  /**
   * Bytes still queued in the socket's send buffer. A bulk producer
   * (e.g. fm's file upload) polls this to apply backpressure: without
   * it the producer dumps the whole payload into the buffer at once,
   * head-of-line blocking later control frames (like a cancel) behind
   * megabytes of data on this single socket. Returns 0 for transports
   * that don't expose it (virtio).
   */
  bufferedAmount(): number {
    return this.ws.bufferedAmount ?? 0;
  }
}
