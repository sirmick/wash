// VirtioConsoleSocket — a WebSocket-shaped wrapper over a v86
// virtio-console port. Lets the existing Conn class talk to a wash
// router inside a v86 VM as if it were a real WebSocket.
//
// The v86 emulator exposes virtio-console as bus events (one pair
// per port): "virtio-console{N}-output-bytes" (VM → host) and
// "virtio-console{N}-input-bytes" (host → VM). v86 may deliver
// output in arbitrary chunks — possibly smaller than one wash frame,
// possibly straddling frame boundaries — so we buffer and reassemble
// length-prefixed frames here before firing onmessage.
//
// The Go-side wash StreamTransport already speaks length-prefixed
// wash frames over any byte stream; this shim is just the JS half
// of that pipe.

export interface V86Bus {
  /** Send a payload tagged with `event` into the v86 emulator. */
  send(event: string, payload: Uint8Array): void;
  /** Subscribe to events emitted by the v86 emulator. */
  register(event: string, handler: (data: Uint8Array | ArrayBuffer | number[]) => void): void;
}

export interface VirtioConsoleSocketOpts {
  bus: V86Bus;
  /** Port index matching /dev/vport0p<N> inside the VM. */
  portN: number;
}

/**
 * Subset of the WebSocket interface that `Conn` actually uses.
 * Implementing this contract is enough for VirtioConsoleSocket to
 * pose as a WebSocket.
 */
export interface SocketLike {
  binaryType: 'arraybuffer';
  onopen: ((ev: Event) => unknown) | null;
  onerror: ((ev: Event) => unknown) | null;
  onmessage: ((ev: MessageEvent) => unknown) | null;
  onclose: ((ev: CloseEvent) => unknown) | null;
  send(data: ArrayBuffer | Uint8Array): void;
  close(code?: number, reason?: string): void;
}

const HEADER_BYTES = 8;

export class VirtioConsoleSocket implements SocketLike {
  binaryType: 'arraybuffer' = 'arraybuffer';
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onmessage: ((ev: MessageEvent) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;

  private readonly bus: V86Bus;
  private readonly portN: number;
  private buf: Uint8Array = new Uint8Array(0);
  private closed = false;

  constructor(opts: VirtioConsoleSocketOpts) {
    this.bus = opts.bus;
    this.portN = opts.portN;
    this.bus.register(`virtio-console${this.portN}-output-bytes`, (chunk) => {
      if (this.closed) return;
      this.onChunk(chunk);
    });
    // Fire onopen asynchronously so any handler attached AFTER
    // `new VirtioConsoleSocket(...)` lands before we call it.
    // Matches WebSocket's "open fires after construction" semantics.
    // Use a plain object cast — constructing a real Event in
    // non-browser test environments (Node) is awkward, and Conn
    // never inspects event fields.
    queueMicrotask(() => {
      if (this.closed) return;
      this.onopen?.({ type: 'open' } as unknown as Event);
    });
  }

  send(data: ArrayBuffer | Uint8Array): void {
    if (this.closed) return;
    const bytes = data instanceof Uint8Array ? data : new Uint8Array(data);
    this.bus.send(`virtio-console${this.portN}-input-bytes`, bytes);
  }

  close(): void {
    if (this.closed) return;
    this.closed = true;
    // Fire onclose on a microtask so caller code doesn't observe a
    // synchronous close-during-construction edge case. Plain object
    // cast: see the onopen note above.
    queueMicrotask(() => {
      this.onclose?.({ type: 'close', code: 1000, reason: '', wasClean: true } as unknown as CloseEvent);
    });
  }

  private onChunk(chunk: Uint8Array | ArrayBuffer | number[]): void {
    const bytes = toUint8(chunk);
    if (bytes.length === 0) return;

    // Append the new chunk to the accumulator. Allocate once;
    // copy-on-each-chunk is fine for the demo workload — the chunks
    // are small (≤ pipe-buffer-size) and frames are usually parsed
    // out immediately.
    const merged = new Uint8Array(this.buf.length + bytes.length);
    merged.set(this.buf, 0);
    merged.set(bytes, this.buf.length);
    this.buf = merged;

    // Drain every complete frame currently in the buffer. We don't
    // call onmessage with partial frames — Conn's onMessage expects
    // exactly one wash frame per dispatch.
    for (;;) {
      if (this.buf.length < HEADER_BYTES) return;
      const length = new DataView(this.buf.buffer, this.buf.byteOffset + 4, 4).getUint32(0, false);
      const total = HEADER_BYTES + length;
      if (this.buf.length < total) return;
      const frame = this.buf.slice(0, total);
      // Advance the buffer FIRST so a re-entrant call into send
      // from onmessage doesn't see a half-consumed buffer.
      this.buf = this.buf.subarray(total);
      this.onmessage?.({ type: 'message', data: frame.buffer } as unknown as MessageEvent);
    }
  }
}

function toUint8(chunk: Uint8Array | ArrayBuffer | number[]): Uint8Array {
  if (chunk instanceof Uint8Array) return chunk;
  if (chunk instanceof ArrayBuffer) return new Uint8Array(chunk);
  return new Uint8Array(chunk);
}

/**
 * Build a SocketLike factory targeting the named v86 virtio-console
 * port. Used by Conn when the page is loaded with
 * `?transport=virtio-console`. The bus must be wired up before the
 * factory is invoked (v86 boots first, then the FE connects).
 */
export function virtioConsoleFactory(bus: V86Bus, portN: number): () => SocketLike {
  return () => new VirtioConsoleSocket({ bus, portN });
}
