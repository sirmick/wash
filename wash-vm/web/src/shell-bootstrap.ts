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
  /** Non-asset wash frames received during bootstrap (re-encoded as
   *  one byte sequence). The caller MUST hand these to the shell's
   *  transport before any further router bytes — otherwise the shell
   *  sees a late catalog after live mutations and renders nothing. */
  replay: Uint8Array;
  /** Call this AFTER the shell has loaded and registered its transport
   *  handler. The bootloader keeps buffering bytes between asset-
   *  complete and finish() so nothing the router emits during the
   *  shell-load gap is lost; finish() flushes replay+buffered bytes
   *  in order via `forward`, then resumes normal fanout. */
  finish: (forward: (bytes: Uint8Array) => void) => void;
}

export interface BootstrapDeps {
  /** Send raw bytes from host → router (i.e., dataVC.input fed byte-by-byte). */
  sendBytes: (bytes: Uint8Array) => void;
  /** Subscribe to raw bytes coming router → host. Returns an unsubscribe. */
  onBytes: (handler: (bytes: Uint8Array) => void) => () => void;
  /** Optional logger; default is a no-op. */
  log?: (line: string) => void;
  /** If true, defer the asset.read send until the first router byte
   *  arrives. Use when the router-side virtio-console port may not
   *  have opened yet — bytes pushed too early can get lost between
   *  the host FIFO and the guest's virtio-console driver. */
  deferUntilFirstByte?: boolean;
}

export async function bootstrapShell(deps: BootstrapDeps, path = '/shell.js', reqID = 1): Promise<BootstrapResult> {
  const log = deps.log ?? (() => {});
  const parser = new FrameParser();
  let assetChannelID = -1;
  let assetSize = 0;
  const assetChunks: Uint8Array[] = [];
  const replayChunks: Uint8Array[] = [];
  // Phase tracks the bootstrap state:
  //   'asset' - asset stream still arriving; parse frames, accumulate
  //   'buffering' - asset complete; capture every incoming byte
  //     verbatim into postAssetBuffer so the caller can replay them
  //     in order after the shell loads
  //   'passthrough' - shell loaded and replay done; future bytes go
  //     straight to caller-supplied forward
  let phase: 'asset' | 'buffering' | 'passthrough' = 'asset';
  const postAssetBuffer: Uint8Array[] = [];
  let forwardFn: ((bytes: Uint8Array) => void) | null = null;

  let resolve!: (r: BootstrapResult) => void;
  let reject!: (e: Error) => void;
  const done = new Promise<BootstrapResult>((res, rej) => { resolve = res; reject = rej; });

  let sent = false;
  const sendRequest = () => {
    if (sent) return;
    sent = true;
    const req = enc.encode(JSON.stringify({ t: 'asset.read', req_id: reqID, path }));
    deps.sendBytes(encodeFrame(0, req));
    log(`bootstrap: sent asset.read path=${path}`);
  };
  let onBytesCount = 0;
  // Persistent across calls so multi-chunk frames assemble correctly.
  const passthroughParser = new FrameParser();
  const detach = deps.onBytes((bytes) => {
    onBytesCount++;
    // Log first 30, then every 10 — generous for debugging.
    if (onBytesCount <= 30 || onBytesCount % 10 === 0) {
      log(`bs: #${onBytesCount} phase=${phase} len=${bytes.length}`);
    }
    if (!sent && deps.deferUntilFirstByte) {
      // First router byte arrived → router is alive, port is open,
      // safe to send. Fire BEFORE feeding the parser so the request
      // can land in the router's input queue ASAP.
      sendRequest();
    }
    if (phase === 'buffering') {
      postAssetBuffer.push(new Uint8Array(bytes));
      return;
    }
    if (phase === 'passthrough') {
      // Frame-decode via the persistent passthroughParser so chunks
      // that span frame boundaries (very common: 8B-only chunks for
      // a header, 32KB chunks for raw bundle bytes) parse correctly.
      try {
        for (const f of passthroughParser.feed(bytes)) {
          if (f.channel === 0) {
            try {
              const msg = JSON.parse(dec.decode(f.payload)) as { t?: string; kind?: string; channel_id?: number; instance_id?: string };
              log(`pt: ctrl t=${msg?.t} kind=${msg?.kind ?? ''} ch=${msg?.channel_id ?? ''} inst=${msg?.instance_id ?? ''}`);
            } catch { log(`pt: ctrl <non-json> len=${f.payload.length}`); }
          } else {
            log(`pt: raw ch=${f.channel} len=${f.payload.length}`);
          }
        }
      } catch (e) { log(`pt: parse threw: ${(e as Error).message}`); }
      forwardFn?.(bytes);
      return;
    }
    // phase === 'asset': parse + classify each frame
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
          // Switch to buffering — capture any bytes that arrive while
          // the caller is loading shell.js so finish() can flush them
          // after the replay.
          phase = 'buffering';
          resolve({
            bytes: out,
            replay,
            finish: (forward) => {
              log(`bs: finish() called, replay=${replay.length}B, buffered=${postAssetBuffer.length} chunks`);
              forwardFn = forward;
              // Frame-decode replay + buffered chunks so we see what
              // the shell will actually receive. The shell only does
              // anything useful on channel.bind {kind:bundle} → raw
              // → channel.unbind, so we should see all three.
              const peekDump = (label: string, bytes: Uint8Array) => {
                try {
                  const fs = new FrameParser().feed(bytes);
                  for (const f of fs) {
                    if (f.channel === 0) {
                      try {
                        const msg = JSON.parse(dec.decode(f.payload)) as { t?: string; kind?: string; channel_id?: number; instance_id?: string };
                        log(`bs: flush[${label}] ctrl t=${msg?.t} kind=${msg?.kind ?? ''} ch=${msg?.channel_id ?? ''}`);
                      } catch { log(`bs: flush[${label}] ctrl <non-json> len=${f.payload.length}`); }
                    } else {
                      log(`bs: flush[${label}] raw ch=${f.channel} len=${f.payload.length}`);
                    }
                  }
                } catch (e) { log(`bs: flush[${label}] parse err: ${(e as Error).message}`); }
              };
              peekDump('replay', replay);
              forward(replay);
              for (let i = 0; i < postAssetBuffer.length; i++) {
                peekDump(`buf${i}`, postAssetBuffer[i]);
                forward(postAssetBuffer[i]);
              }
              postAssetBuffer.length = 0;
              phase = 'passthrough';
              log(`bs: phase=passthrough (after replay+buffered flush)`);
            },
          });
          return; // stop processing more frames in this batch
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

  // Send the asset.read request immediately unless we're deferring
  // until the first router byte (see BootstrapDeps.deferUntilFirstByte).
  if (!deps.deferUntilFirstByte) {
    sendRequest();
  } else {
    log('bootstrap: deferring asset.read until first router byte');
  }

  // Stash detach in a closure that callers won't see — only used on
  // error. On success the bootstrap handler stays subscribed and
  // becomes the passthrough conduit to the shell.
  try {
    return await done;
  } catch (e) {
    detach();
    throw e;
  }
}
