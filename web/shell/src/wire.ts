// Frame codec mirroring internal/wire (WIRE.md §2 + QOS.md §3). Stays
// in lockstep with the Go side by construction.
//
// Flag byte layout:
//   bit 0 = END        (FLAG_END)
//   bits 1..2 = class  (FLAG_CLASS_MASK)
//   bits 3..7 = reserved (FLAG_RESERVED_MASK)

export const FLAG_END = 0x01;
export const FLAG_CLASS_MASK = 0x06;
const FLAG_CLASS_SHIFT = 1;
export const FLAG_RESERVED_MASK = 0xf8;

export const CLASS_INTERACTIVE = 0;
export const CLASS_BULK = 1;
export const CLASS_BACKGROUND = 2;
export const CLASS_CONTROL = 3;
export type Class = 0 | 1 | 2 | 3;

export const MAX_PAYLOAD = 16 * 1024 * 1024;

export interface Frame {
  flags: number;
  channel: number;
  payload: Uint8Array;
}

/** Pack a Class value into flag bits. */
export function flagsWithClass(flags: number, c: Class): number {
  return (flags & ~FLAG_CLASS_MASK) | ((c << FLAG_CLASS_SHIFT) & FLAG_CLASS_MASK);
}

/** Extract the Class value from a flag byte. */
export function classOf(flags: number): Class {
  return ((flags & FLAG_CLASS_MASK) >> FLAG_CLASS_SHIFT) as Class;
}

export function encodeFrame(f: Frame): Uint8Array {
  if (f.payload.length > MAX_PAYLOAD) throw new Error('wash wire: frame too large');
  if (f.channel >= 1 << 24) throw new Error('wash wire: channel id out of range');
  if ((f.flags & FLAG_RESERVED_MASK) !== 0) throw new Error('wash wire: reserved flag set');
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
  if ((flags & FLAG_RESERVED_MASK) !== 0) throw new Error('wash wire: reserved flag set');
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
