# wash wire protocol

Status: **draft, v0.1.** Reflects the post-simplification surface: JSON
everywhere, one envelope per concept, bundles delivered at probe time.
All multi-byte integers are **big-endian**.

## 1. Transports

Two transports carry the same frame format:

- **Browser ↔ router** — a single multiplexed **WebSocket** to the router at
  `ws://127.0.0.1:<port>/ws`, using binary WS frames each carrying exactly
  one wash frame.
- **Router ↔ app** — one **inherited-fd Unix domain socket** per app
  process. The router creates a socketpair, spawns the app with one end as
  **file descriptor 3**, and sets:
  ```
  WASH_PROTO=1
  WASH_APP_ID=<reverse-DNS id, e.g. com.wash.about>
  WASH_INSTANCE_ID=<opaque per-instance string>
  ```
  The SDK adopts fd 3 in `wash_main()` after first checking for
  `--wash-manifest` (§5). No socket paths and no socket auth: trust by fd
  inheritance from a trusted parent.

## 2. Frame format

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|     flags     |              channel id (24 bits)             |
+---------------+-----------------------------------------------+
|                       length (32 bits)                        |
+---------------------------------------------------------------+
|                       payload (length bytes)                  |
+---------------------------------------------------------------+
```

- **flags** (1 byte):
  - Bit 0 (LSB) = `END` (last fragment of a logical message). v0.1
    senders MUST set `END=1`; fragmentation is reserved for future use.
  - Bits 1–2 = **priority class**. `00`=Interactive, `01`=Bulk,
    `10`=Background, `11`=Control. Used by the router's scheduler
    (docs/QOS.md). Interactive is the safe default for any sender
    that doesn't know better.
  - Bits 3–7 = reserved, MUST be 0.
- **channel id** (24 bits). 0 .. 16,777,215.
- **length** (32 bits). Payload byte count. Implementations MUST reject
  frames with `length > 16 MiB` and send an `error` (§13) with code
  `oversize_frame`.

A frame with `length = 0` is legal and reserved for future keep-alive.

## 3. Channel model

A connection carries multiple logical channels distinguished by `channel id`.

### Router ↔ app socket

- **Channel 0** — control. JSON discipline. Implicit on connect.
- **Channel 1** — event channel. JSON discipline. Implicit on connect.
- Channel ids ≥ 2 are dynamic raw channels (apps open them via
  `channel.open` on channel 0; bytes flow as bare-byte frames). See §11.

### Browser ↔ router WebSocket

- **Channel 0** — shell ↔ router control. JSON discipline. Implicit on
  connect.
- Channels ≥ 1 are dynamic raw streams (bundle delivery, asset reads,
  app-owned binary streams). See §11.

## 4. Encoding disciplines

- **JSON (channels 0 and 1):** each frame is one UTF-8 JSON object. Every
  message has a `"t"` (type) string field.
- **Raw:** opaque bytes, no envelope. Used for bundle delivery, asset
  reads, and app-owned binary streams.

JSON is the single application-level codec: app messages, control
messages, event messages, and the shell vocabulary all share one
encoder/decoder pair. (Earlier drafts used CBOR for the event channel;
the dual-codec impedance mismatch produced more bugs than CBOR saved
bytes, so it was removed in favour of one codec everywhere.)

Binary payloads inside app messages travel as JSON-default base64
(encoding/json's `[]byte` rendering); for genuinely large or
performance-critical binary, open a raw channel (§11) instead of inlining
in `app_msg.data`.

## 5. Discovery (`--wash-manifest`)

The router scans configured app directories at load
(default: `~/.local/share/wash/apps`, `/usr/share/wash/apps`; user dir wins
on `id` collision). For each `+x` regular file the router invokes:

```
<binary> --wash-manifest
```

with `WASH_PROTO=1` and an otherwise stripped environment, a 2-second
timeout, and stdout capped at 32 MiB. The SDK intercepts this flag inside
`wash_main` before any app code runs. The binary prints one JSON envelope
to stdout and exits 0.

### 5.1 Probe envelope

```json
{
  "manifest": { … manifest fields, see §5.2 … },
  "bundle_b64": "<base64-encoded index.js>"
}
```

The bundle bytes are the app's embedded FE — historically uploaded on a
post-handshake raw channel, now shipped here so the router caches the
bytes at scan time and there's nothing to negotiate per-instance. Apps
with no FE (CLI helpers, services) omit `bundle_b64`.

### 5.2 Manifest schema

```json
{
  "id": "com.wash.about",
  "name": "About wash",
  "version": "0.0.1",
  "protocol_version": 1,
  "element": "wash-app-about",
  "surface": "window",
  "icon": "data:image/svg+xml,...",
  "instancing": "multi",
  "capabilities": [],
  "window": { "default_width": 480, "default_height": 320 }
}
```

- `id` — reverse-DNS. Regex: `^[a-z0-9][a-z0-9-]*(\.[a-z0-9][a-z0-9-]*)+$`.
- `protocol_version` — ABI gate. Incompatible apps are *listed-disabled
  with a visible reason*, never silently dropped.
- `element` — the custom-element tag the app's web component registers.
  MUST start with `wash-app-` so the global custom-element registry stays
  namespaced.
- `surface` — `"window"` (default; the app appears as a floating window)
  or `"desktop"` (the app's element mounts as the root desktop surface).
  Exactly one running app may declare `surface:"desktop"` — the session
  app.
- `icon` — inline data URI, ≤ 64 KiB. SVG strongly preferred.
- `instancing` — `"multi"` (one process per window, the default) or
  `"singleton"` (at most one running instance globally, addressable by
  `app_id` as a sentinel). A third value `"single"` (one process serves
  many windows) is accepted by the validator but not yet wired in the
  router; treat it as a synonym for `"multi"` for now.
- `capabilities` — declared roles/permissions:
  - `"spawn"` — grants the right to send `spawn.request` on the event
    channel (§9.2).
  - `"prepare_spawn"` — grants the right to send
    `spawn.request{prepare:true}`. The router replies with an attach
    token and binary path; the caller fork+execs the binary itself
    (under sudo, for wash-priv). Distinct from `spawn` because the
    spawner controls how the child is launched, including its uid.
  - `"restart"` — grants the right to send `app.restart` (§9.2) to
    cycle a background singleton service. The target must be
    `surface:"background"`; the router terminates its instance, GCs the
    windows/channels, and spawns a fresh one. Held by the settings app
    (restarts wash-display). See docs/SETTINGS.md §5.
- `window` — hints (`default_width`, `default_height`, `min_width`,
  `min_height`, `resizable`). Honored: width/height. Min sizes / resize
  hints are advisory.

Validation failures (timeout, non-JSON, schema invalid, duplicate id, icon
oversize, bundle base64 malformed) result in a listed-disabled launcher
entry with the reason. Apps are never silently dropped.

## 6. App handshake

After the SDK adopts fd 3:

1. App → router, channel 0:
   ```json
   {"t":"identity","app_id":"com.wash.about","proto":1,"version":"0.0.1","pid":1234}
   ```
   - `pid` is `os.Getpid()`; the router validates against
     `/proc/<pid>/exe` matching the registered binary path.
   - `attach_token` MAY be set when the process was forked by an external
     spawner via `prepare_spawn` (§9.2). The router matches the dial-back
     by token in that case.

2. Router validates `proto`, checks `app_id` matches the spawn record.

3. Router → app, channel 0:
   ```json
   {"t":"identity.ack","instance_id":"i-1","window_id":1,
    "session":{"root":"/sandbox","close_grace_ms":5000}}
   ```
   - `window_id` is assigned for `surface:"window"` apps spawned to show
     a window (default).
   - `window_id` is **omitted** for `surface:"desktop"` apps — the
     element mounts as the root, no floating window.
   - `session` is a bag of router-supplied session facts (omitempty per
     field); apps that don't care can ignore it.
     - `root` — fs sandbox prefix; `""` means unconfined.
     - `close_grace_ms` — how long the app has to reply to
       `window.close_requested` before the router force-kills (§10).

4. The event channel (channel 1) is live from this point.

On any failure, the router sends an `error` (§13) on channel 0 and closes
the socket.

## 7. Bundle delivery

There is no post-handshake bundle upload. The router has the bundle bytes
from the probe envelope (§5.1) and streams them to every (re)attaching
shell over a `kind="bundle"` raw channel:

1. Router → shell, channel 0:
   `{"t":"channel.bind","channel_id":N,"kind":"bundle","instance_id":"i-1"}`
2. Router → shell, channel N: bundle bytes (chunked, Interactive class).
3. Router → shell, channel 0:
   `{"t":"channel.unbind","channel_id":N,"reason":"bundle complete"}`

The shell accumulates the bytes, blob-URL imports the result, and
registers the custom element declared in the manifest.

## 8. Browser ↔ router shell channel (channel 0, JSON)

Carries window/lifecycle control and the FE half of app messages.

**Router → shell:**

- `{"t":"catalog","apps":[…]}` — initial app listing on connect.
- `{"t":"app.declared","instance_id":"…","element":"wash-app-about","surface":"desktop|window","manifest":{…}}`
- `{"t":"session.snapshot","windows":[…],"app_state":{…}}` — the WM
  snapshot the shell rebuilds its window list from.
- `{"t":"session.patch","patches":[…]}` — incremental WM updates
  (window.upsert / window.delete / app_state).
- `{"t":"app_msg.deliver","instance_id":"…","data":<opaque>,"from":{…}?}`
  — a BE-originated message routed to its FE half. `from` is set on
  cross-app deliveries (router-attested).
- `{"t":"notify","instance_id":"…","title":"…","body":"…","level":"info|warn|error"}`
- `{"t":"app.crashed","instance_id":"…","app_id":"…","exit_code":…,"signal":…,"uptime":…,"log":"…"}`
- `{"t":"shell.reload","reason":"…"}` — dev-mode hot reload.
- `{"t":"channel.bind","channel_id":N,"window_id":W,"kind":"bundle|asset|"}`
  — a raw channel is now live. `kind` selects shell-side semantics.
- `{"t":"channel.unbind","channel_id":N,"reason":"…"}`
- `{"t":"asset.read.ok","req_id":…,"channel_id":N,"size":…,"mime":"…"}`
- `{"t":"asset.read.err","req_id":…,"code":"…","msg":"…"}`

**Shell → router:**

- `{"t":"window.close_clicked","window_id":W}` — user clicked close.
  Router relays as `window.close_requested` on the event channel and
  awaits the app's `window.confirm_close` (§10).
- `{"t":"window.focus","window_id":W}` — user clicked / raised; router
  relays to the app as `window.focus`.
- `{"t":"window.move","window_id":W,"x":…,"y":…}`
- `{"t":"window.resize","window_id":W,"w":…,"h":…}`
- `{"t":"window.state","window_id":W,"state":"normal|minimized|maximized"}`
- `{"t":"app_msg.send","instance_id":"…","data":<opaque>,"to":{…}?}` —
  unified FE→BE / cross-app app message. When `to` is set, the router
  resolves the recipient and forwards as `app_msg` on the event channel
  (no router-side `from` attribution — the shell cannot identify the
  originating mounted element). When `to` is unset, the router relays
  to the BE half of the app identified by `instance_id`.
- `{"t":"asset.read","req_id":…,"path":"…"}` — fetch a file from the
  router's embedded asset FS.
- `{"t":"channel.credit","ch":N,"n":…}` — per-channel credit grant
  (docs/QOS.md §5).
- `{"t":"log","level":"…","source":"…","msg":"…","stack":"…"?}` — FE
  log lines mirrored to the BE for visibility.

## 9. App event channel (channel 1, JSON)

Each frame on channel 1 is one JSON object with a `"t"` field.

### 9.1 Router → app

- `{"t":"window.mapped","win":W}` — the window is now visible.
- `{"t":"window.focus","win":W}` — focus gained.
- `{"t":"window.unfocus","win":W}` — focus lost.
- `{"t":"window.resize","win":W,"w":…,"h":…}`
- `{"t":"window.state","win":W,"state":"normal|minimized|maximized"}`
- `{"t":"window.close_requested","win":W}` — user requested close. App
  MUST reply with `window.confirm_close` within `session.close_grace_ms`
  (or the router force-kills).
- `{"t":"shutdown"}` — router is going away; close cleanly within grace.
- `{"t":"app_msg","win":W,"data":<any>,"from":{…}?}` — relayed FE→BE
  or cross-app→BE message. `from` is router-attested and set only on
  cross-app deliveries.
- `{"t":"spawn.ok","app_id":"…","instance_id":"…","req_id":…?,"attach_token":"…"?,"binary":"…"?}`
  — reply to a spawn request. `attach_token` + `binary` are populated
  only for prepared spawns (§9.2).
- `{"t":"spawn.err","app_id":"…","code":"…","msg":"…","req_id":…?}` —
  rejection (codes: `forbidden`, `not_found`, `incompatible_protocol`,
  `internal`).
- `{"t":"app.restart.ok","req_id":…,"instance_id":"…"}` — reply to an
  `app.restart`; `instance_id` is the freshly spawned instance.
- `{"t":"app.restart.err","req_id":…,"code":"…","msg":"…"}` — rejection
  (codes: `forbidden` — missing `restart` cap or non-background target;
  `not_found` — unknown/disabled id; `internal` — respawn failed).
- `{"t":"clipboard.data","req_id":…,"mime":"…","data":"…"}` — reply to
  a `clipboard.get`.
- `{"t":"clipboard.changed","mime":"…"}` — broadcast when any app sets
  the clipboard (recipient is every other app).

### 9.2 App → router

- `{"t":"window.set_title","win":W,"title":"…"}`
- `{"t":"window.confirm_close","win":W,"allow":true|false}` — reply to
  `window.close_requested`.
- `{"t":"spawn.request","app_id":"…","prepare":true|false?,"req_id":…?}`
  — unified spawn request:
  - `prepare:false` (default): router does fork+exec. Requires the
    `spawn` capability. Reply is `spawn.ok{app_id,instance_id}`.
  - `prepare:true`: router mints an attach token + records the pending
    spawn; the caller fork+execs the binary itself with env vars
    `WASH_DISPLAY`, `WASH_PROTO`, `WASH_APP_ID`, `WASH_INSTANCE_ID`,
    `WASH_ATTACH_TOKEN`. Requires the `prepare_spawn` capability.
    `req_id` correlates the reply. Reply is
    `spawn.ok{req_id,instance_id,attach_token,binary}`.
- `{"t":"app.restart","req_id":…,"app_id":"…"}` — cycle a background
  singleton service: the router terminates the named app's running
  instance, GCs its windows/channels, clears its background-started
  flag, and spawns a fresh one. Requires the `restart` capability; the
  target must be `surface:"background"`. `req_id` correlates the reply
  (`app.restart.ok{instance_id}` / `app.restart.err`). See
  docs/SETTINGS.md §5.
- `{"t":"app_msg","win":W,"data":<any>,"to":{…}?}` — unified BE-originated
  message. Without `to`, the router relays to the app's own FE half. With
  `to` ({"app_id":"…"} for singletons or {"instance_id":"…"} for direct),
  the router resolves the recipient and forwards as another `app_msg` on
  the target's event channel with router-attested `from` set.
- `{"t":"notify","title":"…","body":"…?","level":"info|warn|error?"}`
- `{"t":"clipboard.set","mime":"…","data":"…(base64)"}`
- `{"t":"clipboard.get","req_id":…}`
- `{"t":"app_state.set","state":<json>}` — persist FE state blob
  router-side; the shell delivers it as a `wash:state` event on every
  (re)mount.

Capability checks (`spawn`, `prepare_spawn`, `restart`) are enforced by
the router. Denial returns `spawn.err{code:"forbidden"}` (or
`app.restart.err{code:"forbidden"}` for restart); the connection is not
torn down so apps can degrade gracefully.

## 10. Close handshake

1. User clicks the titlebar close (FE). Shell sends
   `window.close_clicked` to the router on the shell channel.
2. Router relays `window.close_requested` to the owning app on its event
   channel.
3. App decides (optionally consulting its FE via `app_msg`) and replies
   `window.confirm_close` with `allow:true|false`.
4. If allowed: router emits a `window.delete` patch in the session
   snapshot stream; the shell tears down the floating window; the app
   process exits.
5. If the app does not reply within `session.close_grace_ms` (default
   5000), the router force-kills.
6. When the router shuts down, it asks the session app to close, which
   cascades to its children via the supervision tree.

## 11. Dynamic raw channels

Either side can open a raw channel for opaque byte streaming (terminal
PTYs, bulk data, app-owned WebRTC-style sockets):

```
App → router, channel 0:   {"t":"channel.open","req_id":N,"window_id":W,"kind":"?"}
Router → app, channel 0:   {"t":"channel.opened","req_id":N,"channel_id":C}
                       or  {"t":"channel.open.err","req_id":N,"code":"…","msg":"…"}
... bare-byte frames on channel C ...
Either side, channel 0:    {"t":"channel.close","channel_id":C}
Router → side, channel 0:  {"t":"channel.closed","channel_id":C,"reason":"…"}
```

Allocated channel ids are ≥ 2 on the app socket and ≥ 1 on the WS. The
router relays raw bytes between the app channel and a matching FE-side
channel (announced to the shell via `channel.bind` on its channel 0).

`kind` selects shell-side semantics: `""` (generic byte pipe, default),
`"bundle"` (router-originated bundle delivery, see §7), `"asset"`
(router-originated, one-shot file from the embedded asset FS; channel
close marks EOF). Apps generally request `""`; the others are
router-internal.

Per-channel credit pacing flows through `channel.credit` (docs/QOS.md §5).

## 12. Versioning

This document specifies `proto = 1`. Additive changes (new message types,
new optional fields ignored when unknown) do **not** bump `proto`.
Wire-incompatible changes do; the manifest's `protocol_version` is the
gate and is checked at handshake.

## 13. Errors

Either side MAY send the following on its control channel (channel 0)
before closing the socket:

```json
{"t":"error","code":"<token>","msg":"<human readable>"}
```

Codes: `proto_mismatch`, `bad_identity`, `bad_frame`, `oversize_frame`,
`bad_manifest`, `forbidden`, `unknown_app`, `internal`, `not_found`,
`incompatible_protocol`, `unknown_channel`, `credit_overflow`,
`bad_request`.

## 14. Worked example

End-to-end trace:

1. **Boot.** Router probes apps via `--wash-manifest`; catalog =
   `{com.wash.session (surface:desktop, caps:[spawn]),
   com.wash.about (surface:window)}`. Each entry caches its bundle
   bytes from the probe envelope.
2. **Session spawn.** Router spawns `com.wash.session`. SDK adopts fd 3,
   sends `identity`. Router replies `identity.ack` with no `window_id`
   (desktop surface) and a `session` bag containing the FS root +
   close-grace.
3. **Desktop mount.** Shell connects WS. Router sends `catalog`,
   `app.declared` for the session instance, opens a `kind=bundle` raw
   channel and streams the cached bytes. Shell blob-imports the bundle,
   registers `wash-app-session`, mounts as the root surface.
4. **User click.** Session FE sends `app_msg.send{instance_id, data}`
   → router → session BE `app_msg{win, data}`.
5. **Spawn About.** Session BE sends `spawn.request{app_id:"com.wash.about"}`
   on its event channel. Router validates the `spawn` capability,
   fork+execs About with the inherited fd, and replies
   `spawn.ok{app_id, instance_id}`.
6. **About handshake.** About's SDK adopts fd 3, sends `identity`, gets
   `identity.ack{instance_id, window_id:1, session:{…}}`.
7. **Window create.** Router emits `app.declared` for About and
   `session.patch{window.upsert}`. Shell streams About's cached bundle
   and mounts `wash-app-about` in a new floating window.
8. **Mapped.** Router sends `window.mapped{win:1}` to About on its event
   channel. About may `window.set_title` if it wants to.
9. **Close.** User clicks close → shell `window.close_clicked{window_id:1}`
   → router `window.close_requested{win:1}` → About
   `window.confirm_close{win:1,allow:true}` → router emits a
   `window.delete` patch + process teardown.

## 15. History

What v0.1 cleaned up from v0.0:

- **Dropped CBOR for the event channel.** Channels 0 and 1 both speak
  JSON; the dual-codec impedance mismatch produced more bugs than the
  byte-count savings justified. C clients no longer need two parsers.
- **Unified `app_msg`.** Previously split into `app_msg` + `app_msg.send.to`
  on the event channel and `app_msg.send` + `app_msg.send.to` on the
  shell channel — same shape, different name. Now: one envelope each,
  with an optional `to`/`from` field.
- **Unified spawn.** `prepare_spawn` was a parallel triplet alongside
  `spawn.request` / `spawn.ok` / `spawn.err` with the same shape but
  different names. Now: one triplet, with `prepare:true` and `req_id`
  driving the external-spawner path.
- **Bundle moved to probe time.** Apps used to upload their FE bundle
  on a post-handshake `kind=bundle` raw channel; the router cached it
  per-instance and replayed to attached shells. Now the bundle ships in
  the probe envelope and the router caches it per-app at scan time. The
  SDK has no upload step; the router has no per-instance bundle state.
- **CloseGraceMs in session bag.** Apps can now read their deadline
  rather than guessing or hard-coding 5 s.

## 16. Deferred

In rough priority order:

- **Fragmentation.** v0.1 mandates `END=1`. Future versions may allow
  splitting a logical message across multiple frames; class bits must
  match across fragments (docs/QOS.md §11).
- **Splice attach/detach.** `splice.attach{src_ch,dst_ch}` /
  `splice.detach`; router copies raw frames between two channels without
  app involvement. Enables zero-overhead pty → window streaming.
- **Service requests.** A formal native-service contract (pty, fs) and
  out-of-process service apps. A service is a capability-gated channel
  opener.
- **Dialog provider role.** `dialog.open` / `dialog.result` carrying
  opaque capability handles.
- **Multi-window apps.** `instancing:"single"` semantics (one process
  serves many windows). Manifest field is accepted today; semantics
  pending.
- **Persistence / reattach.** Router-owned PTY survives socket close;
  reattach by `instance_id`.
- **Window geometry messages.** Drag is shell-local today; live-resize
  events are coalesced; z-order / modal-for are not yet on the wire.
