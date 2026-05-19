# wash wire protocol

Status: **draft, scoped to v0.0** (see [PLAN.md](PLAN.md)). This document
specifies exactly what v0.0 exercises and identifies what is deferred so the
deferred parts slot in without rework. All multi-byte integers are
**big-endian**.

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

- **flags** (1 byte). Bit 0 (LSB) = `END` (last fragment of a logical
  message). v0.0 senders MUST set `END=1`; no fragmentation. Bits 1–7
  reserved, MUST be 0.
- **channel id** (24 bits). 0 .. 16,777,215.
- **length** (32 bits). Payload byte count. Implementations MUST reject
  frames with `length > 16 MiB` and send an `error` (§13) with code
  `oversize_frame`.

A frame with `length = 0` is legal and reserved for future keep-alive.

## 3. Channel model

A connection carries multiple logical channels distinguished by `channel id`.
v0.0 fixes two channels per app connection and a single control channel on
the browser-side connection:

### Router ↔ app socket

- **Channel 0** — control. JSON discipline. Implicit on connect.
- **Channel 1** — event channel. CBOR discipline. Implicit on connect.
- Channel ids ≥ 2 are reserved for raw channels (deferred).

### Browser ↔ router WebSocket

- **Channel 0** — shell ↔ router control. JSON discipline. Implicit on
  connect.
- Channels ≥ 1 are reserved for raw window streams (deferred).

The discipline of channels ≥ 2 (app socket) and ≥ 1 (WS) will be set at OPEN
time in a later revision.

## 4. Encoding disciplines

- **JSON (control channels):** each frame is one UTF-8 JSON object. Every
  message has a `"t"` (type) string field.
- **CBOR (event channel):** each frame is one CBOR map. Every message has a
  `"t"` string field. CBOR is chosen over JSON for the event channel for
  compactness and binary-safe payloads (e.g. `app_msg.data`); chosen over
  protobuf to keep a "nice C API" without codegen.
- **Raw (deferred):** opaque bytes, no envelope.

## 5. Discovery (`--wash-manifest`)

The router scans configured app directories at load
(default: `~/.local/share/wash/apps`, `/usr/share/wash/apps`; user dir wins
on `id` collision). For each `+x` regular file the router invokes:

```
<binary> --wash-manifest
```

with `WASH_PROTO=1` and an otherwise stripped environment, a 2-second
timeout, and stdout capped at 64 KiB. The SDK intercepts this flag inside
`wash_main` before any app code runs. The binary prints one JSON manifest to
stdout and exits 0.

### 5.1 Manifest schema

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
- `protocol_version` — ABI gate. Incompatible apps are *listed-disabled with
  a visible reason*, never silently dropped.
- `element` — the custom-element tag the app's web component registers. MUST
  start with `wash-app-` so the global custom-element registry stays
  namespaced.
- `surface` — `"window"` (default; the app appears as a floating window) or
  `"desktop"` (the app's element mounts as the root desktop surface, not a
  floating window). Exactly one running app may declare `surface:"desktop"`
  — the session app.
- `icon` — inline data URI, ≤ 64 KiB. SVG strongly preferred (themable,
  scalable). Inline because the catalog has no live connection.
- `instancing` — `"multi"` (one process per window, v0.0 default) or
  `"single"` (one process serves many windows, semantics *deferred*).
- `capabilities` — declared roles/permissions. v0.0 recognizes:
  - `"spawn"` — grants the right to send `spawn.request` on the event
    channel (§9.2). The session app's manifest is the v0.0 example.
- `window` — hints (`default_width`, `default_height`, `min_width`,
  `min_height`, `resizable`). v0.0 honors width/height only.

Validation failures (timeout, non-JSON, schema invalid, duplicate id, icon
oversize) result in a listed-disabled launcher entry with the reason. Apps
are never silently dropped.

## 6. App handshake

After the SDK adopts fd 3:

1. App → router, channel 0:
   ```json
   {"t":"identity","app_id":"com.wash.about","proto":1,"version":"0.0.1"}
   ```
2. Router validates `proto`, checks `app_id` matches the spawn record.
3. Router → app, channel 0:
   ```json
   {"t":"identity.ack","instance_id":"…","window_id":1}
   ```
   - `window_id` is assigned for `surface:"window"` apps spawned to show a
     window (v0.0 default).
   - `window_id` is **omitted** for `surface:"desktop"` apps — the element
     mounts as the root, no floating window.
4. The event channel (channel 1) is live from this point.

On any failure, the router sends an `error` (§13) on channel 0 and closes
the socket.

## 7. Asset pull

The shell loads an app's web-component bundle lazily, on first window/desktop
mount. The router relays asset reads from shell to the owning app over the
app's channel 0:

Router → app:
```json
{"t":"asset.read","id":17,"name":"index.js"}
```

App → router (success):
```json
{"t":"asset.read.ok","id":17,"len":12345,"mime":"application/javascript"}
```
… followed by one or more chunks on channel 0 carrying base64 bytes:
```json
{"t":"asset.data","id":17,"bytes":"<base64>","end":false}
{"t":"asset.data","id":17,"bytes":"<base64>","end":true}
```
Receiver concatenates chunks for the same `id` in order; `end:true`
terminates.

App → router (failure):
```json
{"t":"asset.read.err","id":17,"code":"not_found","msg":"…"}
```

> *v0.0 uses base64 inside JSON because raw channels are not yet specified.
> v0.1 replaces this with a per-asset raw side-channel; the router-shell
> interface (§10) absorbs that change without app changes.*

## 8. Browser ↔ router shell channel (channel 0, JSON)

Carries window/lifecycle control between the browser shell runtime and the
router. v0.0 vocabulary:

**Router → shell:**
- `{"t":"app.declared","instance_id":"…","element":"wash-app-about","surface":"desktop|window","manifest":{…}}`
  — the router has accepted a new app instance; the shell should fetch its
  bundle (`{"t":"asset.fetch","instance_id":"…","name":"…"}`) and prepare
  to mount the element.
- `{"t":"window.create","window_id":42,"instance_id":"…","title":"…","w":480,"h":320}`
  — create a floating window of the given size and mount the instance's
  element in it. Skipped for `surface:"desktop"`; the shell mounts the
  element at the root desktop surface instead.
- `{"t":"window.destroy","window_id":42}` — unmount and remove.
- `{"t":"window.title","window_id":42,"title":"…"}` — update title.
- `{"t":"asset.deliver","instance_id":"…","name":"index.js","bytes":"<base64>","end":bool}`
  — bundle data flowing back to the shell.

**Shell → router:**
- `{"t":"asset.fetch","instance_id":"…","name":"index.js"}` — request a
  bundle file (relayed by the router into the app's channel 0 `asset.read`).
- `{"t":"window.close_clicked","window_id":42}` — user clicked the
  titlebar close.
- `{"t":"window.focus","window_id":42}` — user clicked/focused the window;
  shell raises and informs the router (which relays to the app's event
  channel).
- `{"t":"app_msg.send","instance_id":"…","data":<cbor-as-base64-or-json>}`
  — FE-side of an app sending an `APP_MSG` to its BE half (relayed onto
  channel 1 as §9 `app_msg`).

Reserved (deferred): drag/resize geometry, z-order requests, modal-for.

## 9. App event channel (channel 1, CBOR)

Each frame on channel 1 is one CBOR map with a `"t"` string field. v0.0
vocabulary:

### 9.1 Router → app

- `{"t":"window.mapped","win":<u32>}` — the window is now visible.
- `{"t":"window.focus","win":<u32>}` — focus gained.
- `{"t":"window.unfocus","win":<u32>}` — focus lost.
- `{"t":"window.close_requested","win":<u32>}` — user requested close
  (X `WM_DELETE` analogue). App MUST reply with `window.confirm_close`
  within grace (default 5 s) or the router force-kills.
- `{"t":"shutdown"}` — router is going away; close cleanly within grace.
- `{"t":"app_msg","win":<u32>,"data":<any>}` — relayed FE→BE message
  from this app's own frontend half.

### 9.2 App → router

- `{"t":"window.set_title","win":<u32>,"title":<text>}`.
- `{"t":"window.confirm_close","win":<u32>,"allow":<bool>}` — answer to
  `window.close_requested`.
- `{"t":"spawn.request","app_id":<text>}` — **requires `spawn` in
  `capabilities`**. Router replies with one of:
  - `{"t":"spawn.ok","app_id":<text>,"instance_id":<text>}`
  - `{"t":"spawn.err","app_id":<text>,"code":<text>,"msg":<text>}`
    (codes include `forbidden`, `not_found`, `incompatible_protocol`).
- `{"t":"app_msg","win":<u32>,"data":<any>}` — BE→FE message; relayed to
  the app's web component via §8 `app_msg.send` in the reverse direction.

Capability checks are enforced by the router. Without the `spawn`
capability, `spawn.request` is answered with
`{"t":"spawn.err","code":"forbidden"}`; the connection is **not** torn down
so apps can degrade gracefully.

## 10. Close handshake

1. User clicks the titlebar close (FE). Shell sends
   `window.close_clicked` to the router on the shell channel.
2. Router relays `window.close_requested` to the owning app on its event
   channel.
3. App decides (optionally consulting its FE via `app_msg`) and replies
   `window.confirm_close` with `allow:true|false`.
4. If allowed: router sends `window.destroy` to the shell, unmaps the
   window, and (for `multi` instancing) tears down the app process; the
   app's `wash_main` returns.
5. If the app does not reply within grace (5 s default), the router
   force-kills.
6. When the router shuts down, it asks the session app to close, which is
   responsible (via the supervision tree) for asking its children to close
   first.

## 11. Capability gating

The router consults the manifest's `capabilities` list on every
capability-gated request. v0.0 gates exactly one operation: `spawn.request`.
Failures are non-fatal `spawn.err` responses; gated operations never tear
down the connection.

## 12. Versioning

This document specifies `proto = 1`. Additive changes (new message types,
new optional fields ignored when unknown) do **not** bump `proto`.
Wire-incompatible changes do; the manifest's `protocol_version` is the gate
and is checked at handshake.

## 13. Errors

Either side MAY send the following on its control channel (channel 0)
before closing the socket:

```json
{"t":"error","code":"<token>","msg":"<human readable>"}
```

v0.0 codes: `proto_mismatch`, `bad_identity`, `bad_frame`,
`oversize_frame`, `bad_manifest`, `forbidden`, `unknown_app`,
`internal`.

## 14. The v0.0 launch flow (worked example)

End-to-end trace, exercising every v0.0 piece:

1. **Boot.** Router starts; scans apps dir via `--wash-manifest`; catalog
   = `{com.wash.session (surface:desktop, capabilities:[spawn]),
   com.wash.about (surface:window)}`.
2. **Session spawn.** Router spawns `com.wash.session` per config. Inherited
   fd, env set, `wash_main` adopts fd 3, sends `identity`. Router replies
   `identity.ack` with no `window_id` (desktop surface).
3. **Desktop mount.** Shell connects WS. Router sends `app.declared` for
   the session instance with `surface:"desktop"`. Shell fetches the
   bundle (`asset.fetch` → router → app `asset.read` → `asset.read.ok` +
   `asset.data` chunks → router → shell `asset.deliver`). Shell registers
   the `wash-app-session` custom element and mounts it as the root.
4. **User click.** The desktop UI shows a launcher with one entry, "About".
   User clicks. Session FE sends an APP_MSG to its BE via the shell's
   `app_msg.send` → router → session BE `app_msg` event.
5. **Spawn About.** Session BE sends `spawn.request{app_id:"com.wash.about"}`
   on its event channel. Router validates the session's `spawn` capability,
   `fork+exec`s About with inherited fd, and replies `spawn.ok` with the
   new instance id.
6. **About handshake.** About's `wash_main` adopts fd 3, sends `identity`,
   gets `identity.ack` with `window_id=1`.
7. **Window create.** Router emits `app.declared` for About and
   `window.create{window_id:1, …, instance_id, w:480, h:320}` to the shell.
   Shell fetches About's bundle as in step 3 and mounts `wash-app-about`
   in a new floating window.
8. **Mapped.** Router sends `window.mapped{win:1}` to About on its event
   channel. About may `window.set_title` if it wants to.
9. **Interaction.** User clicks the window → shell sends
   `window.focus{window_id:1}` → router relays `window.focus{win:1}` to
   About. User drags the titlebar (shell-local). User clicks close:
10. **Close.** Shell `window.close_clicked{window_id:1}` → router
    `window.close_requested{win:1}` → About `window.confirm_close{allow:true}` →
    router `window.destroy{window_id:1}` to shell + process teardown.

The user can launch About again to confirm two-window focus/raise; killing
the router cleanly tears down the children via the supervision tree.

## 15. Deferred (post-v0.0, designed to slot in)

In rough priority order:

- **Channel lifecycle for dynamic channels.** OPEN / OPENED / CLOSE /
  CLOSED control messages on channel 0; discipline (raw, CBOR-event)
  declared at OPEN. Channel ids ≥ 2 (app socket) and ≥ 1 (WS) become
  dynamic.
- **Raw discipline + credit backpressure.** Bare-bytes frames on raw
  channels; per-channel `channel.credit{ch,n}` on control to grant bytes.
- **Asset pull v2.** Replace base64 `asset.data` with a per-asset raw
  side-channel (router opens a raw channel app→shell that streams the
  bundle).
- **Splice attach/detach.** `splice.attach{src_ch,dst_ch}` /
  `splice.detach`; router copies raw frames between two channels without
  app involvement. Enables zero-overhead pty → window streaming.
- **Service requests.** The native-service contract (pty, fs) and
  out-of-process service apps. A service is a capability-gated channel
  opener.
- **Dialog provider role.** `dialog.open` / `dialog.result` carrying
  opaque capability handles with the lifecycle from
  [ARCHITECTURE.md §Filesystem](ARCHITECTURE.md).
- **Multi-window apps.** Explicit `window.create` request, and
  `instancing:"single"` semantics (one process serves many windows).
- **Persistence / reattach.** Router-owned PTY survives socket close;
  reattach by `instance_id`.
- **Window geometry messages.** Drag/resize/z-order/modal-for; for v0.0,
  drag is shell-local and resize/z-order are deferred.
