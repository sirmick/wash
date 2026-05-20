// Browser-side WebSocket transport: one wash frame per binary
// message (WIRE.md §1). Reconnect is post-v0.0.

import { decodeFrame, encodeCtrl, encodeFrame, FLAG_END, decodeCtrl } from './wire';

export type CtrlHandler = (msg: any) => void;

export class Conn {
  private ws!: WebSocket;
  private handler: CtrlHandler;
  private opening: Promise<void>;

  constructor(url: string, handler: CtrlHandler) {
    this.handler = handler;
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
      // v0.0 reserves WS channels ≥ 1; ignore.
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
}
