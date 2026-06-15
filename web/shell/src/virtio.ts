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
  // Bytes queued in the socket's send buffer but not yet handed to the
  // network. Present on the browser WebSocket; the virtio transport
  // doesn't expose it (and isn't network-throttled the same way), so
  // it's optional and treated as 0 when absent.
  readonly bufferedAmount?: number;
}

const HEADER_BYTES = 8;
// Mirrors wire.MaxPayload (16 MiB). A stream transport must cap the declared
// length itself (the WebSocket path does it in decodeFrame), or a corrupted /
// hostile stream declaring a huge length makes us buffer until OOM.
const MAX_PAYLOAD = 16 * 1024 * 1024;

// Defensive diagnostic sink — used to surface frames being held in
// the buffer because no consumer is attached yet. Normal during
// bootstrap (bus.register-during-ctor re-entry path); the setter on
// `onmessage` drains the buffer as soon as Conn attaches. If this
// ever fires AFTER the shell is up and running, something's wrong.
function vlogWarn(msg: string): void {
  console.warn(`[wash-diag] ${msg}`);
  const sink = (window as unknown as { __washDiagLog?: (s: string, m: string) => void }).__washDiagLog;
  try { sink?.('wash-diag', msg); } catch { /* ignore */ }
}

export class VirtioConsoleSocket implements SocketLike {
  binaryType: 'arraybuffer' = 'arraybuffer';
  onopen: ((ev: Event) => unknown) | null = null;
  onerror: ((ev: Event) => unknown) | null = null;
  onclose: ((ev: CloseEvent) => unknown) | null = null;

  // onmessage uses a setter so frames buffered during construction
  // (before Conn could assign onmessage — see the synchronous
  // bus.register → bootResult.finish() re-entry path in
  // wash-vm/web/src/tinyemu-bridge.ts) get drained on assignment.
  // Without this, real WS semantics aren't preserved: a
  // VirtioConsoleSocket that received bytes during constructor would
  // silently drop frames because the consumer hadn't yet attached.
  private _onmessage: ((ev: MessageEvent) => unknown) | null = null;
  get onmessage(): ((ev: MessageEvent) => unknown) | null { return this._onmessage; }
  set onmessage(fn: ((ev: MessageEvent) => unknown) | null) {
    this._onmessage = fn;
    if (fn != null && this.buf.length >= HEADER_BYTES) this.drainBuffer();
  }

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

    this.drainBuffer();
  }

  // drainBuffer pops every complete wash frame currently in `this.buf`
  // and dispatches it via `_onmessage`. If `_onmessage` is null the
  // buffer stays intact — the setter for `onmessage` re-invokes this
  // method on assignment so any frames buffered while the consumer
  // wasn't attached still get delivered.
  //
  // Why this matters: VirtioConsoleSocket's bus.register call inside
  // the constructor can synchronously receive bytes when there's a
  // bootstrap that flushes accumulated output on handler-registration
  // (tinyemu-bridge.ts → bootResult.finish()). Holding (rather than
  // discarding) frames received before Conn assigns onmessage is what
  // keeps those bytes from being dropped; vlogWarn fires below if a
  // frame is ever held while the shell is already running.
  private drainBuffer(): void {
    while (this.buf.length >= HEADER_BYTES) {
      const length = new DataView(this.buf.buffer, this.buf.byteOffset + 4, 4).getUint32(0, false);
      if (length > MAX_PAYLOAD) {
        vlogWarn(`virtio[port=${this.portN}] frame length ${length} exceeds ${MAX_PAYLOAD} — closing`);
        this.close();
        return;
      }
      const total = HEADER_BYTES + length;
      if (this.buf.length < total) break;
      if (this._onmessage == null) {
        // Hold the frame in the buffer until onmessage attaches. The
        // setter will re-invoke drainBuffer to flush. The warn is
        // chatty during bootstrap (one per buffered frame) but the
        // setter clears the queue in one go, so steady-state silence
        // is the normal post-bootstrap signal.
        vlogWarn(`virtio[port=${this.portN}].FRAME HELD (no onmessage yet): ch=${(this.buf[1] << 16) | (this.buf[2] << 8) | this.buf[3]} payloadLen=${length} bufLen=${this.buf.length}`);
        break;
      }
      const frame = this.buf.slice(0, total);
      // Advance the buffer FIRST so a re-entrant call into send from
      // onmessage doesn't see a half-consumed buffer.
      this.buf = this.buf.subarray(total);
      this._onmessage({ type: 'message', data: frame.buffer } as unknown as MessageEvent);
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
