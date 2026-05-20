// Frame codec mirroring internal/wire (WIRE.md §2). Stays in lockstep
// with the Go side by construction: there are exactly two encodings
// (JSON for channel 0, raw bytes for raw channels), and we only need
// the byte-level framing here.

export const FLAG_END = 0x01;
export const MAX_PAYLOAD = 16 * 1024 * 1024;

export interface Frame {
  flags: number;
  channel: number;
  payload: Uint8Array;
}

export function encodeFrame(f: Frame): Uint8Array {
  if (f.payload.length > MAX_PAYLOAD) throw new Error('wash wire: frame too large');
  if (f.channel >= 1 << 24) throw new Error('wash wire: channel id out of range');
  if ((f.flags & ~FLAG_END) !== 0) throw new Error('wash wire: reserved flag set');
  if ((f.flags & FLAG_END) === 0) throw new Error('wash wire: END flag MUST be set');

  const buf = new Uint8Array(8 + f.payload.length);
  buf[0] = f.flags;
  buf[1] = (f.channel >> 16) & 0xff;
  buf[2] = (f.channel >> 8) & 0xff;
  buf[3] = f.channel & 0xff;
  new DataView(buf.buffer).setUint32(4, f.payload.length, false);
  buf.set(f.payload, 8);
  return buf;
}

export function decodeFrame(bytes: Uint8Array): Frame {
  if (bytes.length < 8) throw new Error('wash wire: short header');
  const flags = bytes[0];
  const channel = (bytes[1] << 16) | (bytes[2] << 8) | bytes[3];
  const length = new DataView(bytes.buffer, bytes.byteOffset, bytes.byteLength).getUint32(4, false);
  if (length > MAX_PAYLOAD) throw new Error('wash wire: oversize frame');
  if ((flags & 0xfe) !== 0) throw new Error('wash wire: reserved flag set');
  if ((flags & FLAG_END) === 0) throw new Error('wash wire: END flag MUST be set');
  if (bytes.length !== 8 + length) throw new Error('wash wire: payload size mismatch');
  return { flags, channel, payload: bytes.subarray(8, 8 + length) };
}

const enc = new TextEncoder();
const dec = new TextDecoder();

export function encodeCtrl(msg: unknown): Uint8Array {
  return enc.encode(JSON.stringify(msg));
}

export function decodeCtrl(payload: Uint8Array): any {
  return JSON.parse(dec.decode(payload));
}
