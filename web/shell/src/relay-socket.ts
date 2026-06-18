// RelayChannelSocket — a WebSocket-shaped transport (SocketLike) that
// carries a remote host's wire over a single raw channel of the browser's
// LOCAL connection to A (docs/REMOTE.md, the "one port" rule).
//
// Outbound (B's RouterClient → here): each wash frame B's Conn produces is
// sent as a raw frame on the relay channel of A's connection; A's router
// splices it verbatim to the ssh -L'd socket reaching B.
//
// Inbound (A → here): A pushes the relay channel's raw frames, whose bytes
// are B's wire arriving in arbitrary chunks. main.tsx feeds them via feed();
// we reassemble length-prefixed wash frames and fire onmessage per frame —
// exactly the role VirtioConsoleSocket plays for the v86 virtio transport.
//
// So B speaks ordinary shell-wire end to end; A is a byte conduit and never
// decodes it, and the browser never opens a second socket.

import { type SocketLike } from './virtio.ts';
import { type Conn } from './ws.ts';

const HEADER_BYTES = 8;
// Mirrors wire.MaxPayload (16 MiB). The real-WebSocket path enforces this in
// decodeFrame; a stream transport must enforce it itself, or a malicious /
// corrupted peer declaring a huge length makes us buffer until OOM.
const MAX_PAYLOAD = 16 * 1024 * 1024;

export class RelayChannelSocket implements SocketLike {
  binaryType: 'arraybuffer' = 'arraybuffer';
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;

  // onmessage uses a setter so any frames fed before the consumer (B's
  // Conn) attached still get delivered when it does (mirrors virtio).
  private _onmessage: ((ev: MessageEvent) => unknown) | null = null;
  get onmessage(): ((ev: MessageEvent) => unknown) | null { return this._onmessage; }
  set onmessage(fn: ((ev: MessageEvent) => unknown) | null) {
    this._onmessage = fn;
    if (fn != null && this.buf.length >= HEADER_BYTES) this.drainBuffer();
  }

  private buf = new Uint8Array(0);
  private closed = false;
  private readonly local: Conn;
  private readonly channelID: number;

  constructor(local: Conn, channelID: number) {
    this.local = local;
    this.channelID = channelID;
    // Fire onopen async so a handler attached after construction lands
    // first (WebSocket semantics). Plain-object cast: Conn never inspects
    // the event, and constructing a real Event in node tests is awkward.
    queueMicrotask(() => {
      if (!this.closed) this.onopen?.({ type: 'open' } as unknown as Event);
    });
  }

  send(data: ArrayBuffer | Uint8Array): void {
    if (this.closed) return;
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    // One B frame → one raw frame on A's relay channel. A writes its bytes
    // to the ssh socket; B reassembles its own wire there.
    this.local.sendRaw(this.channelID, bytes);
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    queueMicrotask(() => {
      this.onclose?.({ type: 'close', code: 1000, reason: '', wasClean: true } as unknown as CloseEvent);
    });
  }

  /** feed accepts a chunk of B's byte stream (A's relay-channel raw bytes). */
  feed(chunk: Uint8Array): void {
    if (this.closed || chunk.length === 0) return;
    const merged = new Uint8Array(this.buf.length + chunk.length);
    merged.set(this.buf, 0);
    merged.set(chunk, this.buf.length);
    this.buf = merged;
    this.drainBuffer();
  }

  // drainBuffer pops every complete wash frame and dispatches it. Holds an
  // incomplete trailing frame until more bytes arrive (A chunks the socket
  // arbitrarily, so a frame can straddle two relay frames).
  private drainBuffer(): void {
    while (this.buf.length >= HEADER_BYTES) {
      const length = new DataView(this.buf.buffer, this.buf.byteOffset + 4, 4).getUint32(0, false);
      if (length > MAX_PAYLOAD) {
        console.error(`wash: relay frame length ${length} exceeds ${MAX_PAYLOAD} — closing`);
        this.onerror?.({ type: 'error' } as unknown as Event);
        this.close();
        return;
      }
      const total = HEADER_BYTES + length;
      if (this.buf.length < total) break;
      if (this._onmessage == null) break; // setter re-drains on attach
      const frame = this.buf.slice(0, total);
      this.buf = this.buf.subarray(total); // advance before dispatch (re-entrancy)
      this._onmessage({ type: 'message', data: frame.buffer } as unknown as MessageEvent);
    }
  }
}
