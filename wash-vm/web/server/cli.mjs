#!/usr/bin/env node
// wash demo admin CLI — connects to the wash demo server's /ws as
// role=admin and sends control/input frames, or tails logs from the
// browser.
//
// Usage:
//   node cli.mjs reload                            # reload all browser tabs
//   node cli.mjs reset                             # restart the VM (FE choice)
//   node cli.mjs dump                              # ask FE to dump CPU+trap state
//                                                  #   (output lands in [tinyemu.stderr])
//   node cli.mjs mem 0x800003c0 64                 # hex-dump RAM at addr (max 256B)
//   node cli.mjs input "uname -a\n"                # legacy console (hvc1)
//   node cli.mjs input --port 1 "ls -l\n"          # hvc1 (login/getty)
//   node cli.mjs input --port 2 "<wash bytes>"     # hvc2 (wash data, binary)
//   node cli.mjs input --port 3 "restart\n"        # hvc3 (wash log/supervisor)
//   node cli.mjs input --port 2 --hex "01 02 ff"   # send raw hex bytes
//   node cli.mjs tail                              # follow logs forever
//   node cli.mjs tail --filter riscv               # only [riscv]-source frames
//
// Env: WASH_SERVER (default ws://localhost:5180/ws)

import { WebSocket } from 'ws';

const SERVER = process.env.WASH_SERVER || 'ws://localhost:5180/ws';
const [verb, ...rest] = process.argv.slice(2);

if (!verb) {
  console.error('usage: wash-rv {reload|reset|stop|run|dump|mem <addr> <len>|input <bytes>|tail [--filter <source>]}');
  process.exit(2);
}

const ws = new WebSocket(SERVER);

ws.on('open', () => {
  ws.send(JSON.stringify({ t: 'hello', role: 'admin' }));

  if (verb === 'reload' || verb === 'reset' || verb === 'stop' || verb === 'run' || verb === 'dump') {
    ws.send(JSON.stringify({ t: 'ctl', verb }));
    ws.close();
    return;
  }

  if (verb === 'mem') {
    const [addr, len] = rest;
    if (!addr || !len) { console.error('mem: usage `wash-rv mem <addr> <len>` (e.g. wash-rv mem 0x800003c0 64)'); process.exit(2); }
    ws.send(JSON.stringify({ t: 'ctl', verb: 'mem', addr, len }));
    ws.close();
    return;
  }

  if (verb === 'input') {
    // Parse --port N and --hex flags. Remaining args concatenated as the payload.
    let port = null, hex = false;
    const args = [];
    for (let i = 0; i < rest.length; i++) {
      if (rest[i] === '--port' && i + 1 < rest.length) { port = parseInt(rest[++i], 10); continue; }
      if (rest[i] === '--hex') { hex = true; continue; }
      args.push(rest[i]);
    }
    const raw = args.join(' ');
    if (!raw) { console.error('input: needs bytes argument'); process.exit(2); }
    let data;
    if (hex) {
      // Parse hex bytes; whitespace/commas separate, '0x' prefix optional.
      const hexBytes = raw.replace(/0x/gi, '').split(/[\s,]+/).filter(Boolean);
      const buf = new Uint8Array(hexBytes.length);
      for (let i = 0; i < hexBytes.length; i++) buf[i] = parseInt(hexBytes[i], 16) & 0xff;
      data = Buffer.from(buf).toString('latin1');
    } else {
      data = raw
        .replace(/\\n/g, '\n')
        .replace(/\\t/g, '\t')
        .replace(/\\r/g, '\r')
        .replace(/\\\\/g, '\\');
    }
    const frame = { t: 'input', data };
    if (port !== null) frame.port = port;
    if (hex) frame.encoding = 'latin1'; // bridge: bytes via charCodeAt
    ws.send(JSON.stringify(frame));
    ws.close();
    return;
  }

  if (verb === 'tail') {
    let filter = null;
    for (let i = 0; i < rest.length - 1; i++) {
      if (rest[i] === '--filter') filter = rest[i + 1];
    }
    ws.on('message', (raw) => {
      let msg;
      try { msg = JSON.parse(raw.toString()); } catch { return; }
      if (msg.t === 'welcome' || msg.t === 'event') {
        console.error(`# ${JSON.stringify(msg)}`);
        return;
      }
      if (filter && msg.source && msg.source !== filter) return;
      if (msg.t === 'log') {
        process.stdout.write(`[${msg.source || 'log'}] ${msg.line || ''}\n`);
      } else if (msg.t === 'stage') {
        process.stdout.write(`[stage${msg.state ? '/' + msg.state : ''}] ${msg.label || ''}\n`);
      }
    });
    // Keep alive forever.
    return;
  }

  console.error(`unknown verb: ${verb}`);
  process.exit(2);
});

ws.on('error', (e) => {
  console.error(`ws error: ${e.message}  (server at ${SERVER})`);
  process.exit(1);
});

ws.on('close', () => {
  if (verb !== 'tail') process.exit(0);
  console.error('# server closed connection');
  process.exit(1);
});
