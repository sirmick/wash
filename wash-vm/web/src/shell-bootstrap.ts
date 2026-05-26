// Bootloader: fetches the wash desktop shell bundle over the wash
// asset channel (in-VM router → embedded FS), instead of loading it as
// a static file from the demo server. Runs once, after the router is
// alive (first byte on the data virtio-console).
//
// Flow:
//   1. Send a single wash control frame on channel 0:
//        { t: "asset.read", req_id: 1, path: "/shell.js" }
//   2. Parse incoming wash frames out of the byte stream:
//        - { t: "asset.read.ok", req_id: 1, channel_id, ... }   → record id
//        - raw frames on channel_id                              → accumulate
//        - { t: "asset.read.err", req_id: 1, code, msg }         → reject
//        - { t: "channel.unbind", channel_id }                   → resolve
//        - everything else                                       → buffer
//   3. Resolve with shell.js bytes + a buffer of non-asset bytes the
//      caller must replay to the (newly-loaded) shell so it doesn't
//      miss the router's initial catalog/snapshot push.
//
// Why parse frames ourselves: the VirtioConsoleSocket class in
// web/shell/src/virtio.ts does the same parse, but using it here would
// register a second listener on the bus alongside the shell's, and
// the wash protocol can't be multiplexed over a single virtio-console
// by two independent wash clients. So this bootloader stays at the
// raw-bytes layer and hands off cleanly.

const HEADER_BYTES = 8;
const FLAG_END = 1;
const CLASS_INTERACTIVE = 1;

function setClassFlag(flags: number, cls: number): number {
  // class lives in bits 1..3 of the flags byte (see internal/wire).
  return (flags & ~0b110) | ((cls << 1) & 0b110);
}

function encodeFrame(channel: number, payload: Uint8Array, flags?: number): Uint8Array {
  const out = new Uint8Array(HEADER_BYTES + payload.length);
  out[0] = flags ?? setClassFlag(FLAG_END, CLASS_INTERACTIVE);
  out[1] = (channel >> 16) & 0xff;
  out[2] = (channel >> 8) & 0xff;
  out[3] = channel & 0xff;
  new DataView(out.buffer).setUint32(4, payload.length, false);
  out.set(payload, HEADER_BYTES);
  return out;
}

interface ParsedFrame { flags: number; channel: number; payload: Uint8Array; }

class FrameParser {
  private buf = new Uint8Array(0);
  feed(chunk: Uint8Array): ParsedFrame[] {
    const merged = new Uint8Array(this.buf.length + chunk.length);
    merged.set(this.buf, 0);
    merged.set(chunk, this.buf.length);
    this.buf = merged;
    const out: ParsedFrame[] = [];
    for (;;) {
      if (this.buf.length < HEADER_BYTES) return out;
      const flags = this.buf[0];
      const channel = (this.buf[1] << 16) | (this.buf[2] << 8) | this.buf[3];
      const length = new DataView(this.buf.buffer, this.buf.byteOffset + 4, 4).getUint32(0, false);
      const total = HEADER_BYTES + length;
      if (this.buf.length < total) return out;
      const payload = this.buf.slice(HEADER_BYTES, total);
      this.buf = this.buf.subarray(total);
      out.push({ flags, channel, payload });
    }
  }
}

const enc = new TextEncoder();
const dec = new TextDecoder('utf-8');

export interface BootstrapResult {
  /** The shell.js bundle bytes, ready for Blob URL + dynamic import. */
  bytes: Uint8Array;
  /** Non-asset wash frames received during bootstrap, re-encoded as a
   *  single byte sequence. Replay these to the shell's transport after
   *  it loads, in the order they arrived, so the shell sees the
   *  router's catalog/session.snapshot/etc. that were sent during our
   *  fetch window. */
  replay: Uint8Array;
}

export interface BootstrapDeps {
  /** Send raw bytes from host → router (i.e., dataVC.input fed byte-by-byte). */
  sendBytes: (bytes: Uint8Array) => void;
  /** Subscribe to raw bytes coming router → host. Returns an unsubscribe. */
  onBytes: (handler: (bytes: Uint8Array) => void) => () => void;
  /** Optional logger; default is a no-op. */
  log?: (line: string) => void;
}

export async function bootstrapShell(deps: BootstrapDeps, path = '/shell.js', reqID = 1): Promise<BootstrapResult> {
  const log = deps.log ?? (() => {});
  const parser = new FrameParser();
  let assetChannelID = -1;
  let assetSize = 0;
  const assetChunks: Uint8Array[] = [];
  const replayChunks: Uint8Array[] = [];

  let resolve!: (r: BootstrapResult) => void;
  let reject!: (e: Error) => void;
  const done = new Promise<BootstrapResult>((res, rej) => { resolve = res; reject = rej; });

  const detach = deps.onBytes((bytes) => {
    for (const f of parser.feed(bytes)) {
      if (f.channel === 0) {
        // JSON ctrl frame — peek `t` to dispatch.
        let msg: { t?: string; req_id?: number; channel_id?: number; size?: number; code?: string; msg?: string } | null = null;
        try { msg = JSON.parse(dec.decode(f.payload)); }
        catch (e) {
          log(`bootstrap: bad ctrl frame: ${(e as Error).message}`);
          replayChunks.push(encodeFrame(0, f.payload));
          continue;
        }
        if (msg?.t === 'asset.read.ok' && msg.req_id === reqID) {
          assetChannelID = msg.channel_id ?? -1;
          assetSize = msg.size ?? 0;
          log(`bootstrap: asset.read.ok ch=${assetChannelID} size=${assetSize}`);
        } else if (msg?.t === 'asset.read.err' && msg.req_id === reqID) {
          reject(new Error(`asset.read.err [${msg.code}]: ${msg.msg ?? ''}`));
        } else if (msg?.t === 'channel.unbind' && msg.channel_id === assetChannelID && assetChannelID >= 0) {
          // Asset stream finished. Concat + resolve.
          const total = assetChunks.reduce((n, c) => n + c.length, 0);
          const out = new Uint8Array(total);
          let off = 0;
          for (const c of assetChunks) { out.set(c, off); off += c.length; }
          const replayTotal = replayChunks.reduce((n, c) => n + c.length, 0);
          const replay = new Uint8Array(replayTotal);
          let roff = 0;
          for (const c of replayChunks) { replay.set(c, roff); roff += c.length; }
          log(`bootstrap: shell.js ${total} bytes, replay ${replayTotal} bytes`);
          resolve({ bytes: out, replay });
        } else if (msg?.t === 'channel.bind' && msg.channel_id === assetChannelID) {
          // No-op: the asset.read.ok already told us about the channel.
        } else {
          // Not ours — re-encode the original frame for replay.
          replayChunks.push(encodeFrame(f.channel, f.payload, f.flags));
        }
      } else if (f.channel === assetChannelID && assetChannelID >= 0) {
        assetChunks.push(new Uint8Array(f.payload));
      } else {
        // Raw bytes on some other channel — buffer for the shell.
        replayChunks.push(encodeFrame(f.channel, f.payload));
      }
    }
  });

  // Send the asset.read request.
  const req = enc.encode(JSON.stringify({ t: 'asset.read', req_id: reqID, path }));
  deps.sendBytes(encodeFrame(0, req));
  log(`bootstrap: sent asset.read path=${path}`);

  try {
    const result = await done;
    return result;
  } finally {
    detach();
  }
}
