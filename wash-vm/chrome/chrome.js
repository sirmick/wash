// wash-vm host chrome (docs/NET.md §8.3). Vanilla ES module, no build step —
// the proxy serves this dir as-is. Two tabs:
//
//   Console — the guest kernel/serial console (LOG plane), streamed from the
//             proxy's /console SSE endpoint.
//   Wash    — the real wash desktop, served BY the VM: this fetches shell.js
//             over the wire (an asset.read on the data plane, exactly like the
//             in-browser demo's shell-bootstrap), then boots the shell over the
//             SAME WebSocket via a virtio-console-style bus so the shell's Conn
//             rides one socket (the proxy bridges one WS at a time).
//
// The wash wire is one length-prefixed frame per message (WIRE.md §1): an
// 8-byte header (flags + 3-byte channel + 4-byte BE length) then payload.

const HEADER = 8;
const FLAG_END = 1;
const CLASS_INTERACTIVE = 1;

function setClassFlag(flags, cls) {
  return (flags & ~0b110) | ((cls << 1) & 0b110);
}

function encodeFrame(channel, payload, flags) {
  const out = new Uint8Array(HEADER + payload.length);
  out[0] = flags ?? setClassFlag(FLAG_END, CLASS_INTERACTIVE);
  out[1] = (channel >> 16) & 0xff;
  out[2] = (channel >> 8) & 0xff;
  out[3] = channel & 0xff;
  new DataView(out.buffer).setUint32(4, payload.length, false);
  out.set(payload, HEADER);
  return out;
}

class FrameParser {
  constructor() { this.buf = new Uint8Array(0); }
  feed(chunk) {
    const merged = new Uint8Array(this.buf.length + chunk.length);
    merged.set(this.buf, 0);
    merged.set(chunk, this.buf.length);
    this.buf = merged;
    const out = [];
    for (;;) {
      if (this.buf.length < HEADER) return out;
      const flags = this.buf[0];
      const channel = (this.buf[1] << 16) | (this.buf[2] << 8) | this.buf[3];
      const length = new DataView(this.buf.buffer, this.buf.byteOffset + 4, 4).getUint32(0, false);
      const total = HEADER + length;
      if (this.buf.length < total) return out;
      out.push({ flags, channel, payload: this.buf.slice(HEADER, total) });
      this.buf = this.buf.subarray(total);
    }
  }
}

const enc = new TextEncoder();
const dec = new TextDecoder('utf-8');

function concat(chunks) {
  let n = 0;
  for (const c of chunks) n += c.length;
  const out = new Uint8Array(n);
  let off = 0;
  for (const c of chunks) { out.set(c, off); off += c.length; }
  return out;
}

function setSpec(text) {
  const el = document.getElementById('spec');
  if (el) el.textContent = text;
}

function wireTabs() {
  const panes = { console: 'pane-console', wash: 'pane-wash' };
  const show = (name) => {
    for (const n of Object.keys(panes)) {
      document.getElementById(panes[n]).style.display = n === name ? '' : 'none';
      document.getElementById('tab-' + n).classList.toggle('active', n === name);
    }
  };
  for (const n of Object.keys(panes)) document.getElementById('tab-' + n).onclick = () => show(n);
  return show;
}

function wireConsole() {
  const pre = document.getElementById('pane-console');
  const es = new EventSource('/console');
  es.onmessage = (ev) => {
    try {
      pre.textContent += atob(ev.data);
      // Cap the buffer so a chatty boot doesn't grow unbounded.
      if (pre.textContent.length > 200000) pre.textContent = pre.textContent.slice(-150000);
      pre.scrollTop = pre.scrollHeight;
    } catch { /* ignore decode hiccups */ }
  };
  es.onerror = () => { /* proxy gone / VM down — leave what we have */ };
}

// bootShell fetches shell.js over the wire and boots the wash desktop over the
// same WebSocket. Returns once the shell bundle has been imported.
async function bootShell(showTab) {
  const ws = new WebSocket(`${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/ws`);
  ws.binaryType = 'arraybuffer';
  await new Promise((res, rej) => {
    ws.onopen = res;
    ws.onerror = () => rej(new Error('data-plane WS error'));
  });

  const parser = new FrameParser();
  let assetCh = -1;
  const assetChunks = [];
  const replayChunks = [];
  let phase = 'asset'; // asset → buffering → passthrough
  const postBuf = [];
  let shellSink = null; // the shell's VirtioConsoleSocket output handler

  const ready = new Promise((resolve, reject) => {
    ws.onmessage = (ev) => {
      const bytes = new Uint8Array(ev.data);
      if (phase === 'buffering') { postBuf.push(bytes); return; }
      if (phase === 'passthrough') { shellSink?.(bytes); return; }
      for (const f of parser.feed(bytes)) {
        if (f.channel === 0) {
          let msg = null;
          try { msg = JSON.parse(dec.decode(f.payload)); }
          catch { replayChunks.push(encodeFrame(0, f.payload, f.flags)); continue; }
          if (msg.t === 'asset.read.ok' && msg.req_id === 1) {
            assetCh = msg.channel_id ?? -1;
          } else if (msg.t === 'asset.read.err' && msg.req_id === 1) {
            reject(new Error(`asset.read.err [${msg.code}] ${msg.msg ?? ''}`));
          } else if (msg.t === 'channel.unbind' && msg.channel_id === assetCh && assetCh >= 0) {
            phase = 'buffering';
            resolve({ shellBytes: concat(assetChunks), replay: concat(replayChunks) });
            return;
          } else if (msg.t === 'channel.bind' && msg.channel_id === assetCh) {
            // no-op — asset.read.ok already told us the channel id
          } else {
            replayChunks.push(encodeFrame(f.channel, f.payload, f.flags));
          }
        } else if (f.channel === assetCh && assetCh >= 0) {
          assetChunks.push(f.payload);
        } else {
          replayChunks.push(encodeFrame(f.channel, f.payload, f.flags));
        }
      }
    };
  });

  // Ask the in-guest router for the shell bundle. The router buffers input, so
  // sending right after open is fine even if its catalog push is in flight.
  ws.send(encodeFrame(0, enc.encode(JSON.stringify({ t: 'asset.read', req_id: 1, path: '/shell.js' }))));

  const { shellBytes, replay } = await ready;
  setSpec(`shell.js ${(shellBytes.length / 1024).toFixed(0)}KB from VM`);

  // Boot the shell over the same socket via a virtio-console-style bus: the
  // shell's pickTransport sees __washShellTransport.kind === 'virtio-console'
  // and builds a VirtioConsoleSocket over window.washV86Bus instead of opening
  // its own WebSocket (the proxy bridges one WS at a time).
  const PORT = 2;
  const outEvent = `virtio-console${PORT}-output-bytes`;
  const inEvent = `virtio-console${PORT}-input-bytes`;
  window.washV86Bus = {
    register(event, handler) { if (event === outEvent) shellSink = (b) => handler(b); },
    send(event, b) { if (event === inEvent) ws.send(b); },
  };
  window.__washShellTransport = { kind: 'virtio-console', port: PORT };

  const url = URL.createObjectURL(new Blob([shellBytes.slice().buffer], { type: 'application/javascript' }));
  try { await import(url); } finally { URL.revokeObjectURL(url); }

  // Hand off: flush the router's connect-time push (catalog/snapshot/desktop
  // declaration buffered during bootstrap), then route live frames to the shell.
  if (shellSink) {
    shellSink(replay);
    for (const b of postBuf) shellSink(b);
    postBuf.length = 0;
  }
  phase = 'passthrough';
  setSpec('wash · served from VM');
  showTab('wash');
}

const showTab = wireTabs();
wireConsole();
bootShell(showTab).catch((e) => {
  console.error('wash-vm chrome:', e);
  setSpec('boot failed: ' + e.message);
  showTab('console');
});
