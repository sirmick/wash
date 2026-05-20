// Browser-side WebSocket transport: one wash frame per binary
// message (WIRE.md §1). Reconnect is post-v0.0.

import { decodeFrame, encodeCtrl, encodeFrame, FLAG_END, decodeCtrl } from './wire';

export type CtrlHandler = (msg: any) => void;
export type RawHandler = (channelID: number, bytes: Uint8Array) => void;

export class Conn {
  private ws!: WebSocket;
  private handler: CtrlHandler;
  private rawHandler: RawHandler;
  private opening: Promise<void>;

  constructor(url: string, handler: CtrlHandler, rawHandler: RawHandler) {
    this.handler = handler;
    this.rawHandler = rawHandler;
    this.opening = new Promise((resolve, reject) => {
      this.ws = new WebSocket(url);
      this.ws.binaryType = 'arraybuffer';
      this.ws.onopen = () => resolve();
      this.ws.onerror = () => reject(new Error('ws error'));
      this.ws.onmessage = (ev) => this.onMessage(ev);
      this.ws.onclose = () => {
        // v0.0: surface in the UI via a non-fatal banner if/when
        // we add one; for now log.
        console.warn('wash: WS closed');
      };
    });
  }

  ready(): Promise<void> { return this.opening; }

  private onMessage(ev: MessageEvent) {
    const f = decodeFrame(new Uint8Array(ev.data));
    if (f.channel !== 0) {
      // Channel ≥ 1: bare bytes on a dynamic raw channel.
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
